# github-release-cache

A minimal, locked-down container that periodically pulls GitHub releases (`releases.json` and matching release assets) and serves them over HTTP for offline/air-gapped devices.

- **Zero external Go dependencies** (pure Go standard library)
- **Distroless runtime** (`gcr.io/distroless/static-debian12:nonroot`, UID `65532`, no shell)
- **Read-only rootfs compatible** (only `/data` needs write access)
- **ETag (`If-None-Match`) support** to avoid wasting GitHub API rate limits
- **Atomic writes** so clients never read partially downloaded files

## Quick Start

```sh
docker build -t github-release-cache .

docker run -d \
  --name github-release-cache \
  --read-only \
  --cap-drop=ALL \
  --security-opt=no-new-privileges:true \
  -p 8080:8080 \
  -v release_data:/data \
  -e GITHUB_REPO="jxlarrea/kiosk-satellite" \
  -e CHECK_INTERVAL="1h" \
  -e FILES="kiosk-satellite-{{.Tag}}.apk,kiosk-satellite-{{.Tag}}.arm64-v8a.apk,kiosk-satellite-{{.Tag}}.armeabi-v7a.apk,kiosk-satellite-{{.Tag}}.x86_64.apk" \
  -e KEEP_RELEASES="3" \
  github-release-cache
```

Files are served from the root HTTP path:

- `http://<host>:8080/releases.json`
- `http://<host>:8080/<asset-filename>`
- `http://<host>:8080/healthz`

## Storage (Named Volume)

Downloaded assets, `releases.json`, and pruning state (`.cache-state.json`) are stored in `DATA_DIR` (default `/data`). Mapping a **named Docker volume** is recommended so cached files persist across container restarts and upgrades, and so the container can run with `--read-only` and non-root UID `65532` without host permission issues:

```sh
docker volume create release_data
docker run -d ... -v release_data:/data github-release-cache
```

In `docker-compose.yml`, declare the named volume and mount it at `/data`:

```yaml
services:
  github-release-cache:
    # ...
    volumes:
      - release_data:/data

volumes:
  release_data:
```

> **Note:** If you use a host bind mount (e.g. `-v /srv/releases:/data`) instead of a named volume, ensure the host directory is writable by UID/GID `65532` (`chown 65532:65532 /srv/releases`).

## Configuration (Environment Variables)

| Variable              | Default           | Description                                                                                                  |
| --------------------- | ----------------- | ------------------------------------------------------------------------------------------------------------ |
| `GITHUB_REPO`         | _(required)_      | Repository in `owner/repo` format (unless `CONFIG_FILE` is set)                                              |
| `GITHUB_TOKEN`        | `""`              | Optional GitHub PAT for private repos or higher rate limits                                                  |
| `CHECK_INTERVAL`      | `1h`              | How often to check GitHub for new releases (Go duration, e.g. `15m`, `1h`, `24h`)                            |
| `CHECK_TIME`          | `12:00:00`        | 24-hour time of day (`HH:MM:SS` or `HH:MM`) to anchor the check schedule                                     |
| `FILES`               | `""` (all assets) | Comma-separated list of file templates/globs                                                                 |
| `FILE_CONTAINS`       | `""` (no filter)  | Comma-separated substrings; only download assets containing at least one (e.g. `arm64-v8a,.dmg`)             |
| `KEEP_RELEASES`       | `3`               | Number of releases whose files are kept on disk (`<= 0` disables pruning)                                    |
| `INCLUDE_PRERELEASES` | `false`           | Include prereleases when selecting the latest release                                                        |
| `INCLUDE_DRAFTS`      | `false`           | Include draft releases (requires `GITHUB_TOKEN`)                                                             |
| `REWRITE_URLS`        | `false`           | Rewrite `browser_download_url` in `releases.json` to point to this server                                    |
| `BASE_URL`            | `""`              | Base URL used when `REWRITE_URLS=true` (e.g. `http://192.168.1.10:8080`; if empty, uses root-relative paths) |
| `PER_PAGE`            | `30`              | Number of releases to include in `releases.json` (`1`-`100`)                                                 |
| `LISTEN_ADDR`         | `:8080`           | HTTP listen address                                                                                          |
| `DATA_DIR`            | `/data`           | Directory where `releases.json` and assets are stored                                                        |
| `LOG_LEVEL`           | `info`            | Log level (`debug`, `info`, `warn`, `error`)                                                                 |
| `CONFIG_FILE`         | `""`              | Optional path to a JSON config file for multi-repo setups                                                    |

### File Templates & Globs

Each entry in `FILES` is evaluated as a Go `text/template` and then matched against the release's assets using `filepath.Match` globbing:

- `{{.Tag}}` — Release tag name (e.g. `v1.2.3`)
- `{{.Version}}` — Tag name with leading `v` stripped (e.g. `1.2.3`)
- `{{.Owner}}` — Repository owner
- `{{.Repo}}` — Repository name

Examples:

- Exact templates: `kiosk-satellite-{{.Tag}}.apk,kiosk-satellite-{{.Tag}}.arm64-v8a.apk`
- Globs: `*.apk` or `app-{{.Version}}-*.tar.gz`
- Empty `FILES`: downloads all assets attached to the latest matching release.

### Multi-Repo JSON Config (`CONFIG_FILE`)

For caching multiple repositories in one container, mount a JSON file and set `CONFIG_FILE=/config/config.json`:

```json
{
  "listen_addr": ":8080",
  "check_interval": "1h",
  "repos": [
    {
      "repo": "jxlarrea/kiosk-satellite",
      "path": "/kiosk-satellite/",
      "files": [
        "kiosk-satellite-{{.Tag}}.apk",
        "kiosk-satellite-{{.Tag}}.arm64-v8a.apk"
      ],
      "keep_releases": 3
    },
    {
      "repo": "owner/other-app",
      "path": "/other-app/",
      "files": ["*.tar.gz"],
      "include_prereleases": true
    }
  ]
}
```
