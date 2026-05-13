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

Kiro CLI sandbox login is not enabled in the default Docker image because the image does not include `kiro-cli` and `sqlite3`. Use local/source mode for that flow, or build a custom Docker image that includes those tools.


## License

[MIT](LICENSE)
