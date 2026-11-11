package store

// SetTestingForTest overrides testing.Testing in Open.
func SetTestingForTest(t interface{ Cleanup(func()) }, v bool) {
	old := isTest
	isTest = func() bool { return v }
	t.Cleanup(func() { isTest = old })
}
