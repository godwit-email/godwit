package core

import "testing"

// An EWS URL keeps its escaped path from discovery to the URL the backend
// is given: escaped once, not twice.
func TestEWSSlotURLRoundTrip(t *testing.T) {
	for _, u := range []string{
		"https://mail.example.com/EWS/Exchange.asmx",
		"https://mail.example.com/Exchange%20Services/Exchange.asmx",
		"http://127.0.0.1:8080/a%2Fb/Exchange.asmx",
	} {
		srv, err := ewsSlot(u)
		if err != nil {
			t.Fatal(err)
		}
		want := u
		if srv.Port == 443 {
			want = "https://mail.example.com:443" + u[len("https://mail.example.com"):]
		}
		if got := slotURL(srv, srv.Path); got != want {
			t.Errorf("ewsSlot(%q) then slotURL = %q, want %q", u, got, want)
		}
	}
}
