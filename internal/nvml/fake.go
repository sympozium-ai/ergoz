package nvml

// Fake is an in-memory Library for tests and simulations. Power/energy are
// settable; energy accrues only through SetEnergy so tests control time.
type Fake struct {
	Devices map[string]*FakeDevice // PCI address → device
}

// FakeDevice implements Device with fixed values.
type FakeDevice struct {
	MW        uint32
	MJ        uint64
	EnergyErr error // e.g. ErrNotSupported for pre-Volta/consumer SKUs
	PowerErr  error
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

func (d *FakeDevice) TotalEnergyMillijoules() (uint64, error) {
	if d.EnergyErr != nil {
		return 0, d.EnergyErr
	}
	return d.MJ, nil
}
