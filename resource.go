package fission

func (f Function) Key() string {
	return f.Metadata.Name
}

func (e Environment) Key() string {
	return e.Metadata.Name
}

func (ht HTTPTrigger) Key() string {
	return ht.Metadata.Name
}
