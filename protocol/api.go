package protocol

// JSON bodies of the relay HTTP API (see README for the endpoint list).

type CreateChannelReq struct {
	HostPub  string `json:"host_pub"`
	Hostname string `json:"hostname"`
	App      string `json:"app,omitempty"`     // "sudo" (default) or "deck"
	HostKX   string `json:"host_kx,omitempty"` // deck: ECDH public key
}

type CreateChannelResp struct {
	Channel   string `json:"channel"`
	HostToken string `json:"host_token"`
}

type HostInfo struct {
	HostPub  string `json:"host_pub"`
	Hostname string `json:"hostname"`
	State    string `json:"state"`
	App      string `json:"app,omitempty"`
	HostKX   string `json:"host_kx,omitempty"`
}

type RegisterPhoneReq struct {
	PhonePub   string `json:"phone_pub"`
	PhoneKX    string `json:"phone_kx,omitempty"`
	DeviceName string `json:"device_name"`
	FCMToken   string `json:"fcm_token"`
	MAC        string `json:"mac"`
}

type RegisterPhoneResp struct {
	PhoneToken string `json:"phone_token"`
}

type PairingInfo struct {
	PhonePub   string `json:"phone_pub"`
	PhoneKX    string `json:"phone_kx,omitempty"`
	DeviceName string `json:"device_name"`
	MAC        string `json:"mac"`
}

type ConfirmReq struct {
	Accept bool `json:"accept"`
}

type ChannelStatus struct {
	State string `json:"state"`
}

type FCMTokenReq struct {
	FCMToken string `json:"fcm_token"`
}

type SubmitResp struct {
	Push string `json:"push"` // "sent", "none" (phone has no push token) or "failed"
}

// RequestState is returned to the host while it waits for the phone.
type RequestState struct {
	State    string    `json:"state"` // pending | seen | answered | cancelled | expired
	Response *Response `json:"response,omitempty"`
}

type RelayConfig struct {
	FCM map[string]string `json:"fcm,omitempty"`
}

const (
	StatePairing = "pairing"
	StateClaimed = "claimed"
	StateActive  = "active"
	ReqPending   = "pending"
	ReqSeen      = "seen"
	ReqAnswered  = "answered"
	ReqCancelled = "cancelled"
	ReqExpired   = "expired"
)

// Deck mailbox: opaque sealed boxes, one inbox per side.

type InboxPost struct {
	Box  string `json:"box"`
	Push bool   `json:"push,omitempty"` // wake the phone via FCM
}

type InboxPostResp struct {
	Seq int64 `json:"seq"`
}

type InboxMessage struct {
	Seq int64  `json:"seq"`
	Box string `json:"box"`
}

type InboxResp struct {
	Messages []InboxMessage `json:"messages"`
}

type BlobResp struct {
	ID string `json:"id"`
}

// TurnCreds are short-lived credentials for the relay's TURN server.
type TurnCreds struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
	TTL        int      `json:"ttl"`
}
