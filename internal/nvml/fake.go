package nvml

// Fake is an in-memory Library for tests and simulations.
type Fake struct {
	Devices map[string]*FakeDevice // PCI address → device
}

// FakeDevice implements Device with a fixed power reading.
type FakeDevice struct {
	MW       uint32
	PowerErr error
}

func (f *Fake) DeviceByPCI(addr string) (Device, error) {
	d, ok := f.Devices[addr]
	if !ok {
		return nil, ErrNotSupported
	}
	return d, nil
}

func (f *Fake) Shutdown() {}

func (d *FakeDevice) PowerMilliwatts() (uint32, error) {
	if d.PowerErr != nil {
		return 0, d.PowerErr
	}
	return d.MW, nil
}
