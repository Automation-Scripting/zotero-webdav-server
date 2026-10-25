# Validação realizada em 24/10/2026

- `python3 .../poc/smoke.py`: 20 casos locais passaram, incluindo headers sem corpo e PUT curl até 32 MiB; hashes conferidos. Arquivos JSON/JSONL nesta pasta são a evidência sanitizada.
- `node --test worker/worker.test.mjs`: 12 testes passaram após correções. Node 25.2.1; teste de stream com substituto Node de FixedLengthStream.
- `node worker/runtime.test.mjs` com Miniflare 4.20260730.0 / workerd 1.20260730.1: stream 12 MiB, stream 32 MiB, redirect 12 MiB, redirect 32 MiB passaram. Runtime oficial local, callbacks simulam API e S3. Streaming comparou SHA-256, Content-Length e ausência de Basic/token/cookie no destino. Nenhuma chamada S3 real.
- Go 1.26.5: `go test ./...` do módulo `infra/handler_webdav` passou. Assinatura local com credenciais fictícias, 32 MiB declarados sem corpo, TTL, destino, ausência de checksum de payload vazio, autenticação, paths e PROPFIND Depth 0.
- Build do handler para `GOOS=linux GOARCH=arm64 CGO_ENABLED=0` passou; binário temporário fora do repositório, não publicado.
- `terraform validate` em `infra/cloudflare` passou, provider Cloudflare 5.24.0; nenhum plan/apply.
- Terraform AWS também validado em cópia isolada em /private/tmp, removendo somente a configuração do backend Terraform Cloud; providers AWS 5.100.0 e archive 2.8.1. Nenhum plan/apply ou acesso ao state remoto.
- `git diff --check` passou. Sintaxe dos scripts Python verificada.

Não executados: S3 real, API Gateway real, Cloudflare público, transferência pelo Zotero real, sincronização entre duas bibliotecas de teste, testes de expiração/retries em serviços reais. Não interpretar testes com mocks como comprovação dessas etapas. Nenhum bucket foi listado ou alterado. Nenhuma senha/URL assinada real foi usada ou registrada.

O processo Zotero lançado usou perfil/dados descartáveis em /private/tmp e foi encerrado. A instância de produção preexistente não foi alterada. A interface de automação não distinguiu as instâncias; portanto não se executou a sondagem no aplicativo.
