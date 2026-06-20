package pkg

type Worker struct{ name string }

func (w Worker) Name() string { return w.name }

func New(n string) Worker { return Worker{name: n} }
