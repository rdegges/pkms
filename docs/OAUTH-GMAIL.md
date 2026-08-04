# Gmail IMAP with your own OAuth client

pkms authenticates to Gmail IMAP with XOAUTH2 using an OAuth client **you
own**. This is a one-time setup that honestly takes **about 30 minutes** —
the consent-screen forms are the slow part, not pkms.

**Check the alternatives first — you may not need OAuth at all:**

- **Personal @gmail.com with 2-Step Verification**: Google still issues
  [app passwords](https://myaccount.google.com/apppasswords). Use
  `auth = "password"` in the ingester and skip this entire document.
- **Fastmail / self-hosted**: app passwords only; OAuth isn't offered to
  third-party IMAP clients. Use `auth = "password"`.
- **Google Workspace**: password/IMAP access is admin-controlled and app
  passwords may be disabled org-wide. OAuth below is usually the only way —
  and your admin must allow the client.

## The 7-day trap (read this first)

A Google OAuth consent screen starts in **Testing** mode. Testing-mode
refresh tokens **expire after 7 days**, which turns a scheduled ingest into
a weekly re-auth chore. You must **publish** the app (Audience → "In
production") for tokens that last. Publishing an unverified app is fine for
your own account: you'll click through an "unverified app" warning once
during `pkms auth`, and Google's verification review is only required for
distributing the client to other people.

If your ingest starts failing about a week after setup with
`invalid_grant`, this is why. Publish the app and re-run `pkms auth`.

## Steps

### 1. Create a Google Cloud project (~5 min)

1. Open <https://console.cloud.google.com/projectcreate>.
2. Name it anything (e.g. `pkms-imap`), create, and wait for it to activate.

You do **not** need to enable the Gmail API — IMAP uses the
`https://mail.google.com/` scope directly, not the Gmail REST API.

### 2. Configure the OAuth consent screen (~10 min)

1. Console → **APIs & Services → OAuth consent screen** (now under
   "Google Auth Platform").
2. Audience: **External**. Fill in the app name and the two email fields —
   nothing else is required.
3. Scopes: you can skip adding scopes here; pkms requests
   `https://mail.google.com/` at authorization time.
4. Finish, then go to **Audience → Publishing status → "Publish app"**.
   Confirm. It will say "needs verification" — for personal use you can
   ignore that; do NOT submit for verification.

### 3. Create the OAuth client (~5 min)

1. Console → **APIs & Services → Credentials → Create credentials →
   OAuth client ID**.
2. Application type: **Desktop app**. Name it `pkms`.
3. Copy the **Client ID** and **Client secret**.

### 4. Configure the ingester

```toml
[[vaults.ingesters]]
type     = "imap"
name     = "gmail"
host     = "imap.gmail.com"
username = "you@gmail.com"
auth     = "xoauth2"
```

### 5. Authorize (~5 min)

```
pkms auth gmail
```

- Prompts for the client ID and secret (stored in your OS keyring), prints
  a Google URL, and waits on a local port.
- Open the URL, pick the account, click through **"Google hasn't verified
  this app" → Advanced → Go to pkms (unsafe)** — that's the unverified-app
  warning for your own client — and approve the mail scope.
- pkms receives the redirect, exchanges the code, and stores the refresh
  token in the keyring. Nothing secret ever touches config.toml.

The browser must run on the **same machine as pkms** — the redirect goes to
`127.0.0.1:<port>` on the pkms host. Over SSH, forward that port
(`ssh -L`), or run `pkms auth` on your desktop and copy the config there.

On a **headless box with no OS keyring**, `pkms auth` can't store the token.
Run it on a machine with a keyring, or skip `pkms auth` entirely and supply
the three secrets as env vars: `PKMS_<VAULT>_<NAME>_OAUTH_CLIENT_ID`,
`_OAUTH_CLIENT_SECRET`, and `_OAUTH_REFRESH_TOKEN` (obtain the refresh token
once on a desktop).

### 6. Verify

```
pkms ingest --source gmail
```

Every later run refreshes a short-lived access token from the stored
refresh token automatically. Tokens die if you revoke access at
<https://myaccount.google.com/permissions> or (Testing mode) after 7 days —
re-run `pkms auth gmail` to recover.

## How pkms treats your mailbox

Read-only, always: pkms opens the mailbox with EXAMINE, fetches with
`BODY.PEEK[]` (your unread mail stays unread), and never sets flags,
deletes, or expunges. Resume state is UIDVALIDITY-aware; if the server
renumbers the mailbox, pkms re-scans and dedup makes the sweep a no-op.
