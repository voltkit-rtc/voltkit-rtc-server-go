// Package voltkit is the VoltKit RTC server SDK for Go: rooms, tokens, chat,
// broadcast streams, usage, billing and verified webhooks, with the standard
// library only.
//
// Your backend does two things: decide who may do what, and hand the client a
// short-lived token. It is never in the media path; there is no stream to
// proxy and nothing that scales with viewers.
//
//	client, err := voltkit.NewStreamClient(voltkit.Config{
//		BaseURL: os.Getenv("VOLTKIT_RTC_URL"),    // from your project's Credentials tab
//		APIKey:  os.Getenv("VOLTKIT_RTC_SECRET"), // server-side only, never sent to a client
//	})
//	if err != nil {
//		log.Fatal(err) // a missing or malformed option, caught at startup
//	}
//
//	room, err := client.EnsureRoom(ctx, voltkit.EnsureRoomParams{RoomID: "show-42", HostIdentity: "user-7"})
//	tok, err := client.CreateToken(ctx, voltkit.CreateTokenParams{RoomID: "show-42", Identity: "user-99", Role: voltkit.RoleViewer})
//
// Every method takes a context.Context first; cancelling it stops the call,
// retries included, and returns the context's error. A call that fails returns
// an *Error carrying the server's Code, the HTTP Status and whether it is
// Retryable, reachable with errors.As; its Message is always safe to log: the
// API key and any URL credentials are redacted before they can reach it. A
// mistake in your own setup (a bad Config, a body JSON cannot carry) is a plain
// error, so a handler cannot mistake it for an answer from VoltKit.
//
// The API key is tenant-wide. It is sent to exactly one place, the controller
// named by Config.BaseURL, in the X-API-Key header: never across a redirect
// (a 3xx fails with the code redirected), never to the rates host, never into
// an error message or a printed client.
//
// Transient failures (a network error, a 429, a 5xx other than
// billing-unavailable) are retried with jittered backoff on every call that
// is safe to repeat; CreateRoom, CreateStream and SendMessage are sent once,
// because a lost reply would turn a success into a spurious "already exists"
// or a duplicate message. The README says what to do then.
//
// Webhooks: VerifyWebhook and ParseWebhook check X-Stream-Signature over the
// raw request body, with a freshness window and a list of secrets for a
// rotation. The broadcast methods are kept for parity with the API and are not
// offered on VoltKit cloud today.
package voltkit
