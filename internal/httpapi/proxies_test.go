package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ratsdev/tvr/internal/core/store"
)

func TestProxyHTTPAndChannelProxyID(t *testing.T) {
	srv, st, _ := newTestServer(t)
	body := `{"name":"SHIPTV","policy":"failover","servers":[{"url":"http://1.2.3.4:9901/udp/"},{"url":"http://2.3.4.5:9902/udp/"}]}`
	res, err := http.Post(srv.URL+"/api/proxies", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var p store.Proxy
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || len(p.Servers) != 2 {
		t.Fatalf("proxy=%+v", p)
	}

	chBody, _ := json.Marshal(map[string]any{
		"name": "CCTV",
		"upstreams": []map[string]string{
			{"url": "239.1.2.3:1234", "proxy_id": p.ID},
		},
	})
	res, err = http.Post(srv.URL+"/api/channels", "application/json", bytes.NewReader(chBody))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("channel status=%d", res.StatusCode)
	}
	var ch store.Channel
	if err := json.NewDecoder(res.Body).Decode(&ch); err != nil {
		t.Fatal(err)
	}
	if ch.Upstreams[0].ProxyID != p.ID {
		t.Fatalf("proxy_id=%q", ch.Upstreams[0].ProxyID)
	}
	if ch.UpstreamURL != "http://1.2.3.4:9901/udp/239.1.2.3:1234" {
		t.Fatalf("primary=%q", ch.UpstreamURL)
	}

	del, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/proxies/"+p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("delete status=%d", res.StatusCode)
	}

	list, err := http.Get(srv.URL + "/api/proxies")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	var proxies []store.Proxy
	if err := json.NewDecoder(list.Body).Decode(&proxies); err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 || proxies[0].ChannelCount != 1 {
		t.Fatalf("list=%+v", proxies)
	}
	_ = st
}

func TestChannelTestWalksProxyFailover(t *testing.T) {
	var hitsDead, hitsLive int
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsDead++
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(dead.Close)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsLive++
		pkt := bytes.Repeat([]byte{0x47}, 188)
		pkt[0] = 0x47
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(bytes.Repeat(pkt, 4))
	}))
	t.Cleanup(live.Close)

	srv, st, _ := newTestServer(t)
	ctx := context.Background()
	p, err := st.CreateProxy(ctx, store.ProxyInput{
		Name:   "SHIPTV",
		Policy: store.ProxyPolicyFailover,
		Servers: []store.ProxyServer{
			{URL: dead.URL + "/udp/"},
			{URL: live.URL + "/udp/"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "CCTV",
		Upstreams: []store.ChannelUpstream{
			{URL: "239.1.2.3:1234", ProxyID: p.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := http.Post(srv.URL+"/api/channels/"+ch.ID+"/test", "application/json", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var env map[string]any
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env["ok"] != true {
		t.Fatalf("test=%+v", env)
	}
	if hitsDead == 0 || hitsLive == 0 {
		t.Fatalf("expected failover walk, dead=%d live=%d", hitsDead, hitsLive)
	}
}

func TestProxyUpdateInvalidatesSession(t *testing.T) {
	up := httptest.NewServer(liveTSHandler())
	t.Cleanup(up.Close)
	next := httptest.NewServer(liveTSHandler())
	t.Cleanup(next.Close)

	srv, st, _ := newTestServer(t)
	ctx := context.Background()
	p, err := st.CreateProxy(ctx, store.ProxyInput{
		Name:    "SHIPTV",
		Servers: []store.ProxyServer{{URL: up.URL + "/udp/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChannel(ctx, store.ChannelInput{
		Name: "CCTV",
		Upstreams: []store.ChannelUpstream{
			{URL: "239.1.2.3:1234", ProxyID: p.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx2, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx2, http.MethodGet, srv.URL+"/stream/"+ch.ID, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, 188)
	if _, err := io.ReadFull(res.Body, buf); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"name":    "SHIPTV",
		"policy":  store.ProxyPolicyFixed,
		"servers": []map[string]string{{"id": p.Servers[0].ID, "url": next.URL + "/udp/"}},
	})
	put, err := http.NewRequest(http.MethodPut, srv.URL+"/api/proxies/"+p.ID, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	put.Header.Set("Content-Type", "application/json")
	upd, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	defer upd.Body.Close()
	if upd.StatusCode != 200 {
		b, _ := io.ReadAll(upd.Body)
		t.Fatalf("put status=%d body=%s", upd.StatusCode, b)
	}

	deadline := time.Now().Add(2 * time.Second)
	gotErr := false
	for time.Now().Before(deadline) {
		if _, err := res.Body.Read(buf); err != nil {
			gotErr = true
			break
		}
	}
	if !gotErr {
		t.Fatal("proxy update should invalidate the live session")
	}
}
