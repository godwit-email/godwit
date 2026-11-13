package core

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

	"github.com/godwit-email/godwit/core/plugins"
)

// StoreProduct is Godwit on the Microsoft Store, apps.microsoft.com/detail/9NS3Q2CGWD12.
const StoreProduct = "9NS3Q2CGWD12"

// StoreLatestVersion is the newest version of Godwit published on the
// Microsoft Store, as 1.21.0, asked of the Store's catalog
// (displaycatalog.mp.microsoft.com) through Proxy, for the About page of the
// Store's package.
// shortcut: displaycatalog is undocumented; if it changes this errors and About only offers the Store's page.
func (s *Service) StoreLatestVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://displaycatalog.mp.microsoft.com/v7.0/products/"+StoreProduct+"?market=US&languages=en-US", nil)
	if err != nil {
		return "", err
	}
	// The proxy resolves the Store's name itself: some networks resolve only there.
	transport := &http.Transport{Proxy: func(r *http.Request) (*url.URL, error) { return s.Proxy(r.URL), nil }}
	res, err := (&http.Client{Transport: transport, Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("store catalog: %s", res.Status)
	}
	return storeNewest(io.LimitReader(res.Body, 4<<20))
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
