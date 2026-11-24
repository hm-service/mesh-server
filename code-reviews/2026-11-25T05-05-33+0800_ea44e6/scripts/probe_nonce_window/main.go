// Probe: the nonce cache keeps a nonce for ClockSkew measured from the moment
// the server saw it, but CheckTimestamp also accepts timestamps up to ClockSkew
// in the future. A signed request that arrives early (client clock ahead) can
// therefore be replayed after its nonce entry expired while its timestamp is
// still inside the accepted window.
//
// Run with:
//
//	go run ./code-reviews/2026-11-25T05-05-33+0800_ea44e6/scripts/probe_nonce_window
package main

import (
	"fmt"
	"strconv"
	"time"

	"mesh-server/core"
)

func main() {
	base := time.Unix(1_732_400_000, 0)
	// The device clock is 300s ahead, so the signature is stamped well in the
	// future of the server clock and is still accepted on arrival.
	ts := base.Add(core.ClockSkew).Unix()
	stamp := strconv.FormatInt(ts, 10)

	first := base
	signedAt, arrivalErr := core.CheckTimestamp(stamp, first)
	cache := core.NewNonceCache(core.ClockSkew)
	claimed := cache.Claim("nonce-replay-probe", first, signedAt)
	fmt.Printf("FIRST  now=%s ts=%d timestampErr=%v claimed=%v\n", first.UTC().Format(time.RFC3339), ts, arrivalErr, claimed)

	// The nonce entry expires at first+300s. The same request is replayed one
	// second after that, still inside [ts-300s, ts+300s].
	replay := first.Add(core.ClockSkew + time.Second)
	replaySignedAt, replayErr := core.CheckTimestamp(stamp, replay)
	replayClaimed := cache.Claim("nonce-replay-probe", replay, replaySignedAt)
	fmt.Printf("REPLAY now=%s timestampErr=%v claimed=%v\n", replay.UTC().Format(time.RFC3339), replayErr, replayClaimed)

	if replayErr == nil && replayClaimed {
		fmt.Println("REPRODUCED: the replayed signature is accepted as unused although it was already used")
		return
	}
	fmt.Println("NOT REPRODUCED")
}
