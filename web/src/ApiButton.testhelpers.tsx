type UIButton = Proto.v1alpha1UIButton
type UIInputSpec = Proto.v1alpha1UIInputSpec
type UIInputStatus = Proto.v1alpha1UIInputStatus

export function textField(
  name: string,
  defaultValue?: string,
  placeholder?: string
): UIInputSpec {
  return {
    name: name,
    label: name,
    text: {
      defaultValue: defaultValue,
      placeholder: placeholder,
    },
  }
}

export function boolField(name: string, defaultValue?: boolean): UIInputSpec {
  return {
    name: name,
    label: name,
    bool: {
      defaultValue: defaultValue,
    },
  }
}

export function hiddenField(name: string, value: string): UIInputSpec {
  return {
    name: name,
    hidden: {
      value: value,
    },
  }
}

export function makeUIButton(args?: {
  inputSpecs?: UIInputSpec[]
  inputStatuses?: UIInputStatus[]
}): UIButton {
  return {
    metadata: {
      name: "TestButton",
    },
    spec: {
      text: "Click Me!",
      iconName: "flight_takeoff",
      inputs: args?.inputSpecs,
    },
    status: {
      inputs: args?.inputStatuses,
    },
  }
}
