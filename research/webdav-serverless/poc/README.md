# Laboratório isolado

Sem AWS por padrão. `lab.py` usa exclusivamente 127.0.0.1:18765 e arquivos em `/private/tmp/zotero-webdav-lab`. A autenticação `lab / lab-only` é fictícia, só para loopback. As URLs locais têm HMAC e expiram em 120 s; **não são assinaturas AWS**. O destino usa `localhost`, enquanto a origem usa `127.0.0.1`, para testar mudança de host e remoção de Authorization. Essa topologia HTTP local não comprova o caso HTTPS/S3 público.

```sh
python3 research/webdav-serverless/poc/prepare.py
python3 research/webdav-serverless/poc/lab.py --root /private/tmp/zotero-webdav-lab
```

Em outro terminal:

```sh
python3 research/webdav-serverless/poc/smoke.py
```

`prepare.py` cria ZIPs sintéticos de 1, 7, 12 e 32 MiB mais overhead ZIP, perfil e diretório de dados separados. Não executar novamente enquanto o perfil estiver aberto; ele regrava os fixtures. `smoke.py` testa 307/308 por curl, 413 dos modelos de buffering e resposta recebida após enviar apenas headers, sem corpo. Os 20 casos passaram; evidências sanitizadas em `../results/`. Não usar `curl -v`, `--trace`, `--location-trusted` ou publicar logs de debug com URLs assinadas.

## Zotero real (pendente)

No macOS:

```sh
/Applications/Zotero.app/Contents/MacOS/zotero -no-remote -profile /private/tmp/zotero-webdav-lab/profile
```

Confirmar que é o perfil novo e que o diretório de dados é `/private/tmp/zotero-webdav-lab/data`. Não cadastrar conta zotero.org nem habilitar sync da biblioteca de produção. Abrir Ferramentas → Desenvolvedor → Executar JavaScript, marcar execução assíncrona e colar `zotero-probe.js`. O script aborta se o diretório não for o descartável. Não é plugin instalado: é uma sondagem de homologação que usa os mesmos helpers HTTP usados pelo cliente.

A sondagem testa XHR PUT com File e `Zotero.HTTP.download`, salva apenas status/tamanhos/versão; comparar hashes de `download-*` com `manifest.json`. O log do receptor registra somente presença de Authorization, jamais o valor. Confirme PUT final, Content-Type, quantidade de bytes, ausência de Basic no destino, hash e 200. O laboratório rejeita Authorization no destino assinado.

Para verificar também a UI nativa, configurar WebDAV do perfil descartável com base `http://127.0.0.1:18765/r307/`, credenciais fictícias e “Verificar servidor”; repetir em `r308`. O Zotero acrescenta `zotero/`. Esse teste usa `.prop` pequeno e não prova upload grande. Uma sincronização completa entre duas bibliotecas sintéticas exige conta/ambiente de teste autorizado e deve ser executada separadamente; a sondagem HTTP não substitui esse aceite.

A sessão desta investigação abriu e encerrou somente o processo descartável. A ferramenta de controle de UI retornou a instância de produção preexistente; por isso nenhum script foi executado nessa instância e não há `zotero-results.json` comprovado.

## S3 e entrada AWS reais (não executados)

O modo opcional `lab.py --s3-bucket NOME-webdav-poc` usa boto3 instalado separadamente para assinar PUT/GET de `.zip` sob `webdav-poc/zotero/`, usando TTL 120 s e Content-Type explícito. Não provisiona bucket. Exige bucket descartável já autorizado, privado, na região correta, com credenciais temporárias restritas pela política de exemplo. Nome com sufixo de teste é uma trava adicional, não substitui IAM. `.prop` continua local nesse modo: serve para sondar transferências, não é servidor de homologação completo.

**Não executar esse modo nesta etapa:** PUT/GET reais podem gerar custos. Nunca usar credenciais de produção ou logs completos. Não imprimir respostas de presign. A política `iam-example.json` é apenas para o teste de ZIP e não concede listagem/exclusão de produção.

`lambda_probe.py` é um handler independente para uma futura função descartável: recebe evento API Gateway/Function URL, valida Basic, assina apenas `/zotero/probe-N.zip` no prefixo de teste e registra somente request ID, método e tamanho do evento. Não implementa verificação DAV. Usar uma função/HTTP API nova sob autorização futura, nunca substituir o Lambda de produção por esse diagnóstico.

Matriz futura AWS: corpo binário de 1, 4, 5, 7, 12 e 32 MiB; capturar status do ingresso e existência/ausência de invocação Lambda; repetir com Content-Length e sem ele. Confirmar base64/envelope em casos aceitos. Não concluir que o handler executou só porque houve log do authorizer. Uma função que retorna 307 sem ler `event.body` ainda recebeu um evento materializado.

Matriz futura Cloudflare: executar o Worker implementado em homologação, `stream` e `redirect`; ZIPs 7/12/32/90 MiB, limite do plano, e >100 MB. Em 307, medir com envio lento quanto foi transmitido antes da resposta; teste de “só headers” deve receber 307 antes de transmitir o corpo para confirmar resposta antecipada naquele ingresso/protocolo. Repetir com Zotero real e TLS público. Não elevar o teto de aplicação ou declarar bypass com base apenas no laboratório local.
