package local

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

// A controller that reads the Tilt data model and creates new Cmd objects.
//
// Reads the Cmd Status.
//
// A CmdServer offers two constraints on top of a Cmd:
//
// - We ensure that the old Cmd is terminated before we replace it
//   with a new one, because they likely use the same port.
//
// - We report the Cmd status Terminated as an Error state,
//   and report it in a standard way.
type ServerController struct {
	createdCmds        map[string]*Cmd
	createdTriggerTime map[string]time.Time
	deletingCmds       map[string]bool

	cmdCount int
}

var _ store.Subscriber = &ServerController{}

func NewServerController() *ServerController {
	return &ServerController{
		createdCmds:        make(map[string]*Cmd),
		createdTriggerTime: make(map[string]time.Time),
		deletingCmds:       make(map[string]bool),
	}
}

func (c *ServerController) OnChange(ctx context.Context, st store.RStore, summary store.ChangeSummary) {
	if summary.IsLogOnly() {
		return
	}

	servers := c.determineServers(ctx, st)
	for _, server := range servers {
		c.reconcile(ctx, server, st)
	}
}

func (c *ServerController) determineServers(ctx context.Context, st store.RStore) []CmdServer {
	state := st.RLockState()
	defer st.RUnlockState()

	// Find all the Cmds owned by CmdServer.
	//
	// Simulates controller-runtime's notion of Owns().
	ownedCmds := make(map[string]*Cmd)
	for _, cmd := range state.Cmds {
		for _, ref := range cmd.ObjectMeta.OwnerReferences {
			if ref.Kind != "CmdServer" {
				continue
			}

			ownedCmds[ref.Name] = cmd
		}
	}

	var r []CmdServer

	// Infer all the CmdServer objects from the legacy EngineState
	for _, mt := range state.Targets() {
		if !mt.Manifest.IsLocal() {
			continue
		}
		lt := mt.Manifest.LocalTarget()
		if lt.ServeCmd.Empty() ||
			mt.State.LastSuccessfulDeployTime.IsZero() {
			continue
		}

		name := mt.Manifest.Name.String()
		cmdServer := CmdServer{
			ObjectMeta: ObjectMeta{
				Name: name,
			},
			Spec: CmdServerSpec{
				Args:           lt.ServeCmd.Argv,
				Dir:            lt.ServeCmd.Dir,
				Env:            lt.ServeCmd.Env,
				TriggerTime:    mt.State.LastSuccessfulDeployTime,
				ReadinessProbe: lt.ReadinessProbe,
			},
		}

		cmd, ok := ownedCmds[mt.Manifest.Name.String()]
		if ok {
			cmdServer.Status = CmdServerStatus{
				CmdName:   cmd.Name,
				CmdStatus: cmd.Status,
			}
		}

		r = append(r, cmdServer)
	}

	return r
}

func (c *ServerController) reconcile(ctx context.Context, server CmdServer, st store.RStore) {
	cmdSpec := CmdSpec{
		Args:           server.Spec.Args,
		Dir:            server.Spec.Dir,
		Env:            server.Spec.Env,
		ReadinessProbe: server.Spec.ReadinessProbe,
	}

	name := server.Name
	created, isCreated := c.createdCmds[name]
	triggerTime := c.createdTriggerTime[name]
	if isCreated && equality.Semantic.DeepEqual(created.Spec, cmdSpec) && triggerTime.Equal(server.Spec.TriggerTime) {
		// We're in the correct state! Nothing to do.
		return
	}

	// Otherwise, we need to create a new command.

	// If the command hasn't appeared yet, wait until it appears.
	if isCreated && server.Status.CmdName == "" {
		return // wait for the command to appear
	}

	// If the command is running, wait until it's deleted
	if isCreated && server.Status.CmdStatus.Terminated == nil {
		if !c.deletingCmds[name] {
			c.deletingCmds[name] = true
			st.Dispatch(CmdDeleteAction{Name: server.Status.CmdName})
		}
		return
	}

	// Start the command!
	c.createdTriggerTime[name] = server.Spec.TriggerTime
	c.deletingCmds[name] = false
	c.cmdCount++

	cmdName := fmt.Sprintf("%s-serve-%d", name, c.cmdCount)
	spanID := SpanIDForServeLog(c.cmdCount)

	cmd := &Cmd{
		ObjectMeta: ObjectMeta{
			Name: cmdName,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name: name,
					Kind: "CmdServer",
				},
			},
			Annotations: map[string]string{
				v1alpha1.AnnotationSpanID: string(spanID),
			},
			Labels: map[string]string{
				v1alpha1.LabelManifest: name,
			},
		},
		Spec: cmdSpec,
	}
	c.createdCmds[name] = cmd
	st.Dispatch(CmdCreateAction{Cmd: cmd})
}

type CmdServer struct {
	metav1.ObjectMeta

	Spec   CmdServerSpec
	Status CmdServerStatus
}

type CmdServerSpec struct {
	Args           []string
	Dir            string
	Env            []string
	ReadinessProbe *v1alpha1.Probe

	// Kubernetes tends to represent this as a "generation" field
	// to force an update.
	TriggerTime time.Time
}

type CmdServerStatus struct {
	CmdName   string
	CmdStatus CmdStatus
}