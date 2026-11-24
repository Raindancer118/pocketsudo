# PocketSudo

Approve `sudo` on your Linux machine with the fingerprint on your Android phone, from anywhere.

```
$ sudo pacman -Syu
pocketsudo: confirm on S24 FE (code 42) or touch the fingerprint reader – Enter for password
pocketsudo: opened on S24 FE
pocketsudo: approved via S24 FE
```

The phone shows the exact command, the terminal and the same two-digit code. The local fingerprint
reader keeps working in parallel, and the password always remains as fallback.

## How it works

```
notebook (PAM helper) ──signed request──▶ relay ──FCM wake-up──▶ phone
        ◀──────── signed approval ────────────────────────────── (BiometricPrompt + Keystore key)
```

- The notebook signs every request with its own key; the phone shows nothing that isn't signed.
- The phone signs approvals with a P-256 key in the Android Keystore that only works after a strong
  biometric and dies when a new finger is enrolled.
- The relay sees both signatures but holds no key that could approve anything. A compromised relay
  can delay or drop requests, never grant one. Push messages only carry ids.
- Pairing uses a QR code with a secret the relay never sees, plus a 6-digit code shown on both sides.

## Install

On the machine (root):

```sh
sudo ./install-host.sh pocketsudo-linux-amd64   # from the release
sudo pocketsudo pair          # scan the QR code with the phone camera or the app
sudo pocketsudo test          # round trip without touching PAM
sudo pocketsudo install-pam   # adds one line to /etc/pam.d/sudo
```

Keep a root shell open while trying it the first time. `sudo pocketsudo uninstall-pam` restores the file.
On the phone: install `pocketsudo.apk` from the latest release.

Settings: `pocketsudo set timeout 60`, `set local-fingerprint auto|off`, `set remote-only true` (phone only
for SSH sessions). `pocketsudo status` shows pairings and PAM state.

## Relay

`docker compose up -d` with a `.env` containing `FCM_SERVICE_ACCOUNT_JSON`, `FCM_API_KEY`, `FCM_APP_ID`,
`FCM_SENDER_ID` (from your Firebase project). Without them it works, but phones only see requests while
the app is open. CI deploys `main` to `pocketsudo.raindancer118.de`.

## Development

```sh
go test -race ./...
UPDATE_VECTORS=1 go test ./internal/protocol   # refresh the Go→Java test vectors
cd android && ./gradlew testDebugUnitTest lintDebug assembleDebug
```
