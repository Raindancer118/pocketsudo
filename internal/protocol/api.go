package protocol

// JSON bodies of the relay HTTP API (see README for the endpoint list).

type CreateChannelReq struct {
	HostPub  string `json:"host_pub"`
	Hostname string `json:"hostname"`
}

type CreateChannelResp struct {
	Channel   string `json:"channel"`
	HostToken string `json:"host_token"`
}

type HostInfo struct {
	HostPub  string `json:"host_pub"`
	Hostname string `json:"hostname"`
	State    string `json:"state"`
}

type RegisterPhoneReq struct {
	PhonePub   string `json:"phone_pub"`
	DeviceName string `json:"device_name"`
	FCMToken   string `json:"fcm_token"`
	MAC        string `json:"mac"`
}

type RegisterPhoneResp struct {
	PhoneToken string `json:"phone_token"`
}

type PairingInfo struct {
	PhonePub   string `json:"phone_pub"`
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
	StatePairing   = "pairing"
	StateClaimed   = "claimed"
	StateActive    = "active"
	ReqPending     = "pending"
	ReqSeen        = "seen"
	ReqAnswered    = "answered"
	ReqCancelled   = "cancelled"
	ReqExpired     = "expired"
)
