package main

import (
	"strings"
	"testing"
)

func TestStoreNewest(t *testing.T) {
	// trimmed from displaycatalog.mp.microsoft.com/v7.0/products/9NS3Q2CGWD12
	const doc = `{"Product":{"DisplaySkuAvailabilities":[{"Sku":{"Properties":{"Packages":[
		{"PackageFullName":"BaranWang.Godwit_1.9.0.0_neutral_~_fghk7gtg13c98"},
		{"PackageFullName":"BaranWang.Godwit_1.21.0.0_neutral_~_fghk7gtg13c98"},
		{"PackageFullName":"BaranWang.Godwit_1.10.0.0_neutral_~_fghk7gtg13c98"}]}}}]}}`
	if v, err := storeNewest(strings.NewReader(doc)); err != nil || v != "1.21.0" {
		t.Fatalf("storeNewest = %q, %v; want 1.21.0", v, err)
	}
	if _, err := storeNewest(strings.NewReader(`{"Product":{}}`)); err == nil {
		t.Fatal("storeNewest without packages: want an error")
	}
}
