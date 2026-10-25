# WebDAV: Cloudflare Worker + Lambda existente + S3 privado

Implementação preparada em 24/10/2026, **sem deploy**. O caminho de download continua sendo o 307 que o Lambda já produz. O Worker devolve esse 307 ao Zotero, sem seguir a URL e sem transportar o ZIP de download.

Para uploads há duas opções, selecionadas por `upload_mode` no Terraform:

| Modo | Percurso do arquivo | Estado |
|---|---|---|
| `stream` (padrão) | Zotero → Worker → S3 | Não exige replay de PUT pelo cliente; aguarda confirmação do S3. Homologação remota pendente. |
| `redirect` | Zotero → Worker: PUT inicial; Worker → Zotero: 307; Zotero → S3: PUT repetido | Código não lê o corpo; compatibilidade real do Zotero e resposta antecipada da borda ainda precisam ser comprovadas. |

O redirecionamento **não é uma promessa de upload ilimitado nem de zero bytes na Cloudflare**. O cliente pode enviar bytes antes de receber o 307. A Cloudflare documenta 100 MB por requisição nos planos Free/Pro, 200 MB no Business, até 5 GB ajustáveis no Enterprise. Isso depende do plano da conta/zona, não da assinatura Workers Paid. Não aumente `max_upload_bytes` sem confirmar o limite contratado. [Limites oficiais](https://developers.cloudflare.com/workers/platform/limits/).

```mermaid
sequenceDiagram
    participant Z as Zotero original
    participant W as Cloudflare Worker
    participant L as API Gateway + Lambda atual
    participant S as S3 privado
    Z->>W: PUT /zotero/KEY.zip + Basic + tamanho
    W->>L: POST /_worker/upload (metadados + Basic + segredo interno)
    L->>L: Valida credenciais, caminho e tamanho; assina PUT
    L-->>W: URL SigV4 válida por 120 s
    alt stream
        W->>S: PUT em streaming, sem Basic/segredo interno
        S-->>W: 200 após armazenar objeto
        W-->>Z: 200
    else redirect (homologação)
        W-->>Z: 307 + Location + no-store
        Z->>S: PUT com mesmo corpo e Content-Type
        S-->>Z: 200
    end
    Z->>W: GET /zotero/KEY.zip
    W->>L: GET + Basic (redirect manual)
    L-->>W: 307 existente para S3
    W-->>Z: Mesmo 307
    Z->>S: GET direto
```

O Zotero usa **um único endereço**. Portanto o Worker também encaminha as operações leves ao backend, mas toda a lógica DAV continua no Go. `PROPFIND` e `MKCOL` são encapsulados em POST para o endpoint `/_worker/dav`, porque não podemos pressupor suporte desses verbos no API Gateway. GET/HEAD/OPTIONS/DELETE são encaminhados diretamente. Todos os PUT, inclusive `.prop` e o arquivo de verificação, usam o assinador; não existe ZIP em JSON, base64 ou corpo de invocação Lambda.

## O que já foi implementado

- [Worker](worker.mjs), sem dependências de produção, sem logs de credenciais ou URLs assinadas.
- [Assinador e transporte DAV](../infra/handler_webdav/worker_bridge.go) no Lambda existente. Exigem Basic válido **e** segredo interno com pelo menos 32 caracteres. O endpoint fica desabilitado quando o segredo está vazio.
- [Terraform Cloudflare unificado](../infra/cloudflare_worker.tf) no mesmo workspace/state `infra/`, conectando automaticamente o Worker ao API Gateway, S3 e segredo `webdav_worker_token`.
- [Configuração Lambda](../infra/lambda_webdav.tf) para o segredo e expiração de PUT. S3, caminhos e formato dos anexos continuam os existentes.
- Testes de contrato Go e Node. O teste de contrato Node usa um substituto de `FixedLengthStream`. Também passaram quatro casos no runtime local oficial workerd/Miniflare: 12 e 32 MiB em cada modo, com S3 simulado. Em streaming verificaram Content-Length, hash e ausência de credenciais no destino. Não comprovam a borda Cloudflare pública, o cliente Zotero ou a AWS.

A role do Lambda existente já tem `s3:PutObject` em `zotero/*`: não são necessárias access keys AWS na Cloudflare, nova role de upload ou permissões públicas no bucket. O segredo do Worker não substitui a autenticação do usuário. A resposta assinada usa o hostname regional do S3, evitando redirects adicionais do S3.

## O que você precisa configurar fora do código

1. Ter uma conta Cloudflare e habilitar/escolher seu subdomínio `workers.dev` no painel. Não é necessário transferir seu domínio nem contratar plano pago para começar.
2. Criar um API token restrito à conta, com a permissão de edição de Workers Scripts necessária aos recursos Terraform; disponibilizá-lo como `CLOUDFLARE_API_TOKEN` no executor Terraform. Não cole o token no chat ou no repositório.
3. Informar `cloudflare_account_id` e `cloudflare_workers_subdomain` no workspace Terraform (`webdav-server`). A URL do API Gateway e o hostname regional do S3 são lidos automaticamente dos recursos AWS no mesmo state.
4. Gerar um segredo aleatório de pelo menos 32 caracteres e cadastrá-lo como `webdav_worker_token` (variável sensível) no workspace Terraform. Ele alimenta simultaneamente a Lambda e o Worker.
5. Após homologação, configurar o endereço base `https://WORKER.SUBDOMINIO.workers.dev` no Zotero, mantendo o usuário e senha existentes. O Zotero acrescenta `/zotero/`; não duplicar esse segmento.

Posso preparar código, testes, Terraform, integração de CI e revisar planos. A criação/login da conta, emissão/armazenamento de tokens e escolha de cobrança ficam na sua conta. **Nenhuma implantação ou mudança na biblioteca foi executada nesta etapa.**

## Implantação futura, após homologação isolada

Não executar os exemplos contra produção durante a investigação. O diretório `infra` usa o workspace Terraform Cloud `webdav-server` e `env=prod` por padrão. Não executar `apply` indiscriminadamente: já havia alterações locais, inclusive versionamento do bucket suspenso.

1. Criar um ambiente de homologação separado (bucket/prefixo, Lambda, API e state próprios), quando autorizado a criar recursos. Usar somente anexos sintéticos.
2. Compilar/testar o Go com a versão do `go.mod`: `go test ./...` em `infra/handler_webdav`. O workflow atual de GitHub Actions já faz testes/build/deploy do Lambda ao enviar alterações para `prod`; **não fazer push para `prod` antes da liberação**.
3. Incluir o segredo e expiração de PUT (120 s) na configuração Lambda. O Terraform existente ignora `filename`/`source_code_hash`: apenas aplicar Terraform **não atualiza o código**. Publicar o novo binário pelo workflow já existente ou pelo processo de deploy homologado.
4. Configurar no workspace `webdav-server` do Terraform Cloud a variável de ambiente sensível `CLOUDFLARE_API_TOKEN` e as variáveis de Terraform `cloudflare_account_id`, `cloudflare_workers_subdomain` e `webdav_worker_token`. State contém o segredo mesmo sendo `sensitive` e `secret_text`: proteger acesso, backups e criptografia.
5. Executar `terraform init`, `terraform validate`, `terraform plan` em `infra/`; revisar antes de `apply`.
6. Usar `stream` inicialmente. Validar verificação, upload/download e integridade na matriz abaixo. Depois testar `redirect`; só promovê-lo se o Zotero real passar.
7. Escolher o endpoint de produção e migrar um computador por vez, conforme o plano de reversão do parecer.

Não usar Access com tela interativa, desafios de navegador ou regras de cache para essa rota DAV: o cliente nativo precisa receber 401 Basic, XML e códigos DAV. Evitar WAF que bloqueie PROPFIND. TLS permanece habilitado, bucket privado, sem URLs permanentes.

## Critérios de aceite antes de produção

- Verificar servidor no Zotero original: OPTIONS/DAV, PROPFIND, inexistente → 404, PUT/GET/DELETE de `zotero-test-file.prop`.
- ZIP sintético incompressível de 1, 7, 12, 32 e 90 MiB; medir **tamanho do ZIP transmitido**, não só do anexo. Testar limite configurado e um arquivo acima de 100 MB: falha explícita esperada, sem declarar bypass.
- Dois perfis descartáveis: upload real pelo primeiro, download real pelo segundo, SHA-256 igual. Verificar `.prop`, mtime, hash, sobrescrita, exclusão e nomes Unicode dentro do ZIP.
- No modo 307: testar `Content-Length`, ausência/presença de `Expect: 100-continue`, conexão lenta e resposta antes de enviar o arquivo inteiro; contar bytes enviados ao primeiro destino. O sucesso de curl não comprova replay pelo Zotero.
- Não pode chegar Basic, Cookie ou token interno ao S3. Em streaming os headers de saída são criados do zero. Em 307, testar se o Zotero remove Basic ao mudar de domínio; não depender apenas da regra HTTP abstrata.
- Expirar URL antes do início → falha; repetir pedido ao Worker → nova URL. Não reutilizar URL expirada. Interromper PUT, repetir sem corrupção e verificar que `.prop` só é publicado após sucesso do ZIP.
- Respostas 403, 413, 429, 5xx, cancelamento e retries: nenhum sucesso antecipado. O Worker não repete silenciosamente streams já consumidos. O cliente deve reiniciar a operação.
- CloudWatch: no POST assinador, tamanho em dezenas/centenas de bytes, nunca ZIP/base64. Não habilitar log de evento completo, query, Location ou corpo da resposta. Desabilitado observability de invocações no Worker; revisitar política de logs da plataforma antes de habilitá-la.
- A API de assinatura aceita até 5.000.000.000 bytes, mas o Worker aplica seu teto menor. Esse teto da aplicação não aumenta quotas da plataforma.

Em streaming, `FixedLengthStream` preserva o comprimento e evita chunked encoding no PUT ao S3. Corpo desconhecido é rejeitado com 411; precondições não implementadas são rejeitadas, não ignoradas. A Cloudflare documenta que definir `Content-Length` manualmente não basta. [Request](https://developers.cloudflare.com/workers/runtime-apis/request/), [FixedLengthStream](https://developers.cloudflare.com/workers/runtime-apis/streams/transformstream/).

## Testes reproduzíveis

```sh
node --test worker/worker.test.mjs
# Opcional: teste no runtime local oficial (instala apenas dependências de teste)
npm ci --prefix worker
npm run test:runtime --prefix worker
cd infra/handler_webdav
go test ./...
```

Os scripts do [laboratório](../research/webdav-serverless/poc/README.md) validam separadamente o cliente e a entrada HTTP. Não incluem recursos pagos implantados. As evidências e limitações estão no [parecer](../research/webdav-serverless/parecer.md).
