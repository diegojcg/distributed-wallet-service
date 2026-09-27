//go:build integration

package platform

import (
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"net/http"
	"testing"
	"time"
)

func TestFxLifecycle(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	var server *HTTPServer
	app := fxtest.New(t, Module(), fx.Populate(&server))
	app.RequireStart()
	addr := server.Listener.Addr().String()
	c := http.Client{Timeout: time.Second}
	res, err := c.Get("http://" + addr + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	app.RequireStop()
	if res, err = c.Get("http://" + addr + "/health/live"); err == nil {
		res.Body.Close()
		t.Fatal("listener remains open")
	}
}
