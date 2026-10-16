package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"time"
)

func main() {
	targets := []string{
		"https://wappass.baidu.com/wp/api/login",
		"https://pan.baidu.com/api/gettemplatevariable?fields=[%22bdstoken%22]",
		"https://pcs.baidu.com/rest/2.0/pcs/quota?method=info",
		"https://www.baidu.com/",
	}
	for _, t := range targets {
		start := time.Now()
		client := &http.Client{Timeout: 20 * time.Second}
		req, err := http.NewRequest("GET", t, nil)
		if err != nil {
			fmt.Printf("REQERR %-70s %v\n", t, err)
			continue
		}
		req.Header.Set("User-Agent", "netdisk;P2SP;3.0.0")
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("FAIL   %-70s %-9s %v\n", t, time.Since(start).Round(time.Millisecond), err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		resp.Body.Close()
		fmt.Printf("OK     %-70s %-9s http=%d body=%q\n", t, time.Since(start).Round(time.Millisecond), resp.StatusCode, string(body))
	}

	// TLS capabilities actually negotiated.
	fmt.Printf("\nDefault TLS config: min=%v\n", tls.VersionName(0))
}
