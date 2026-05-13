# Kiro-Go VPS Deployment

This fork is prepared for VPS deployment with Docker Compose.

Main additions in this version:

- Proxy pool with per-account proxy binding
- Proxy failure backoff and status panel
- Kiro / CodeWhisperer / AmazonQ endpoint selection with fallback
- Upstream Kiro-Go 1.0.7 merge
- Sanitized example config for public repositories

The detailed deployment guide is maintained in Chinese:

[README_CN.md](README_CN.md)


## Quick Start

```bash
git clone https://github.com/scvxzf1/Kiro-Go.git
cd Kiro-Go
mkdir -p data
cp data/config.example.json data/config.json
docker compose up -d --build
```

Open:

```text
http://YOUR_VPS_IP:8080/admin
```

Before production use, edit `data/config.json` and change:

- `password`
- `requireApiKey`
- `apiKey`

Do not commit `data/config.json`; it contains account tokens and proxy credentials.


## Docker Notes

The API service, admin panel, account pool, and proxy pool work with Docker.

Kiro CLI sandbox login can work in Docker, but the container must be able to execute `kiro-cli`.

The image includes `sqlite3`; it does not include `kiro-cli`. Install `kiro-cli` on the host, mount it into the container, and set `KIRO_CLI_PATH`:

```yaml
volumes:
  - ./data:/app/data
  - /usr/local/bin/kiro-cli:/usr/local/bin/kiro-cli:ro
environment:
  - CONFIG_PATH=/app/data/config.json
  - KIRO_CLI_PATH=/usr/local/bin/kiro-cli
```

Verify inside the container:

```bash
docker compose exec kiro-go sqlite3 --version
docker compose exec kiro-go sh -lc '$KIRO_CLI_PATH --version'
```

If the mounted binary depends on other files or host libraries, mount its full install directory or run the project in source mode on the host.


## License

[MIT](LICENSE)
