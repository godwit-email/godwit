package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/sysproxy"
)

// storeProduct is Godwit on the Microsoft Store, apps.microsoft.com/detail/9NS3Q2CGWD12.
const storeProduct = "9NS3Q2CGWD12"

// storeLatest asks the Store's catalog for a published version newer than
// this one: "" when up to date.
// shortcut: displaycatalog is undocumented; if it changes this errors and About only offers the Store's page.
func storeLatest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://displaycatalog.mp.microsoft.com/v7.0/products/"+storeProduct+"?market=US&languages=en-US", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		Proxy: func(r *http.Request) (*url.URL, error) { return sysproxy.For(r.URL), nil },
	}}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("store catalog: %s", res.Status)
	}
	v, err := storeNewest(io.LimitReader(res.Body, 4<<20))
	if err != nil || plugins.CompareVersions(v, app.Version) <= 0 {
		return "", err
	}
	return v, nil
}

// storeNewest is the newest package version in a catalog product, as
// 1.21.0: the Store keeps older packages listed beside it.
func storeNewest(r io.Reader) (string, error) {
	var doc struct {
		Product struct {
			DisplaySkuAvailabilities []struct {
				Sku struct {
					Properties struct {
						Packages []struct{ PackageFullName string }
					}
				}
			}
		}
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return "", err
	}
	newest := ""
	for _, a := range doc.Product.DisplaySkuAvailabilities {
		for _, p := range a.Sku.Properties.Packages {
			// Name_Version_Arch_ResourceId_PublisherId; MSIX versions carry a fourth part, always 0
			parts := strings.Split(p.PackageFullName, "_")
			if len(parts) < 2 {
				continue
			}
			v := strings.TrimSuffix(parts[1], ".0")
			if newest == "" || plugins.CompareVersions(v, newest) > 0 {
				newest = v
			}
		}
	}
	if newest == "" {
		return "", errors.New("store catalog: no package")
	}
	return newest, nil
}
