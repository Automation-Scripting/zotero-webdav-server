# Parecer técnico — Zotero WebDAV, Lambda, S3 e Cloudflare

Data: 24/10/2026. Escopo inicial: avaliar serverless sem modificar produção. Escopo posteriormente autorizado: preparar a implementação Cloudflare + Lambda existente, sem deploy. Consulte [implementação e configuração](../../worker/README.md).

**Conclusão:** o Lambda atual pode continuar fazendo autenticação, metadados e redirects de download. Acrescentamos assinatura de PUT a partir de um POST pequeno. O upload precisa entrar por outra plataforma: um 307 produzido pelo handler Lambda não remove os limites de entrada do API Gateway/Lambda. Cloudflare Workers é uma alternativa concreta para arquivos dentro de seu limite de entrada; entrega implementada com streaming por padrão e redirect 307 experimental. Não está comprovado que o Zotero instalado repita PUT entre domínios nem que a borda Cloudflare entregue o redirect antes de receber a carga inteira. Não há garantia de arquivos arbitrariamente grandes no plano gratuito.

## Grau de evidência

| Resultado | Classificação |
|---|---|
| Servidor Go transmite `request.Body` ao S3; Lambda tradicional materializa corpo/base64 | Código local inspecionado |
| GET já devolve 307 com URL pré-assinada em ambos os servidores | Código local inspecionado; execução de produção não consultada |
| Requisitos de verificação/sincronização abaixo | Código do Zotero 10.0.6 instalado, BuildID 20261007113827, Gecko 140; hashes registrados em `results/installed-source.json` |
| Limites AWS/Cloudflare | Documentação oficial consultada em 24/10/2026 |
| Curl consegue PUT 307/308 de até 32 MiB no laboratório | 20 testes locais passaram; SHA-256 comparado para uploads aceitos |
| Lambda assina PUT de 32 MiB sem receber arquivo; bloqueia pedidos inválidos | Testes Go locais com credenciais fictícias, sem acesso AWS |
| Worker 307 não consome corpo; streaming encaminha ao destino sem Basic/token; GET mantém 307 | Testes locais automatizados; não equivalem a homologação de produção |
| Streaming/307 com 12 e 32 MiB no workerd/Miniflare oficial | Quatro testes locais passaram; S3/API simulados, Content-Length e hash conferidos no streaming |
| Fluxo real Zotero → Cloudflare → S3 | **Pendente**; nenhuma implantação paga ou acesso ao bucket foi realizado |
| Repetição de PUT/GET 307/308 pelo Zotero real | **Pendente**. Perfil descartável criado, mas a ferramenta de UI selecionou a instância de produção; a execução nela foi evitada |

Os modos `buffer6`/`buffer10` do laboratório são modelos deliberadamente simplificados. Seu 413 local não é evidência de comportamento exato da AWS, base64, bytes recebidos pela borda ou ordem de execução de authorizers.

## Contrato do Zotero

A fonte primária examinada foi extraída do `omni.ja` do Zotero instalado. Referências públicas: [WebDAV no repositório oficial](https://github.com/zotero/zotero/blob/master/chrome/content/zotero/xpcom/storage/webdav.js) e [HTTP no repositório oficial](https://github.com/zotero/zotero/blob/master/chrome/content/zotero/xpcom/http.js). `master` pode mudar; os números abaixo referem-se aos arquivos da versão instalada.

| Fase | Comportamento observado na fonte |
|---|---|
| Verificação, `checkServer`, linhas 900–1065 | OPTIONS no diretório Zotero aceita 200/204/404, mas exige header DAV. PROPFIND Depth 0 com XML pede getcontentlength e aceita 207/404. |
| Arquivo inexistente | GET `nonexistent.prop` precisa resultar em 404. 200 com página HTML não serve. |
| Teste de escrita | PUT `zotero-test-file.prop`, corpo de um espaço, aceita 200/201/204; GET aceita 200/404 e exige existência; DELETE aceita 200/204. |
| Diretório ausente | PROPFIND no pai; criação por MKCOL exige 201 (`_createServerDirectory`, aproximadamente 1640). Raiz virtual existente evita esse ramo, mas servidor compatível deve atender a criação. |
| Credenciais | PROPFIND captura autenticação; Basic é reenviado explicitamente em requisições posteriores. Fonte também suporta Digest, mas servidor atual só oferece Basic sobre TLS. |
| Upload, aproximadamente 812–889 | Remove `.prop` anterior; cria ZIP local; envia File por XMLHttpRequest PUT com `Content-Type: application/zip`, sem timeout global de upload. Não há negociação prévia de URL S3, multipart S3 ou divisão em vários objetos. |
| Conclusão do upload, 1550–1605 | Só após sucesso do ZIP envia `.prop` XML versão 1 com mtime em milissegundos e hash MD5 do anexo. PUT `.prop` aceita 200/201/204. |
| Download, 620–685 | `Zotero.HTTP.download`, nesta versão implementado com fetch, aceita 200/404. ZIP ausente pode levar à remoção do `.prop` órfão. |
| Metadados, 1440–1530 | GET `.prop` aceita 200/300/404; 300/404 significam ausência. Também lê formato legado com timestamp em segundos. |
| Limpeza, 1310–1390 e 1700–1785 | PROPFIND Depth 1, 207 XML com href/getlastmodified; DELETE de ZIP/PROP, aceitando ausência. |

HEAD é útil e já está implementado, mas não apareceu como requisito do caminho WebDAV principal inspecionado. Não encontramos LOCK/UNLOCK, MOVE, COPY ou PROPPATCH nesse fluxo: `.prop` é um objeto XML acessado por GET/PUT, não uma propriedade DAV gravada por PROPPATCH. Isso não faz do servidor um WebDAV genérico completo.

WebDAV é destinado à biblioteca pessoal; bibliotecas de grupo usam Zotero Storage. [Documentação oficial de sincronização](https://www.zotero.org/support/sync).

## Redirects, autenticação e repetição

O helper HTTP segue redirects por padrão, salvo `followRedirects:false` (http.js ~187, 352). O upload usa XHR com um File; o download usa fetch com Authorization Basic quando existem credenciais na URI. O código do Zotero não contém um tratamento próprio que comprove o replay seguro de PUT 307/308 para S3: esse comportamento depende também de Gecko, origem, TLS e possibilidade de reenviar o corpo.

307/308 preservam método pela semântica HTTP; isso não prova implementação do cliente, remoção de Authorization, replay após upload parcial ou ausência de prompt. Preferimos 307 + `Cache-Control:no-store`: 308 é permanente e indesejável para URLs efêmeras. Não usar 301/302/303 para upload.

PUT S3 bem-sucedido responde 200, aceito pelo Zotero. GET S3 responde 200. Redirect não deve terminar como 307 visível ao helper; deve chegar ao código final. Usar hostname regional correto, método assinado correto, Content-Type e Content-Length compatíveis e nenhum checksum de corpo vazio gerado pelo SDK. A implementação desativa checksum opcional no presigner, não valida conteúdo por MD5 de ETag e não exige cabeçalhos que o Zotero desconheça.

A fonte HTTP prevê repetição de 429/5xx e Retry-After; o modo WebDAV usa intervalos de 2,5 s e janela de 3 s, enquanto a verificação desativa retries. Erros 403 de URL expirada não devem ser tratados como se a mesma URL voltasse a funcionar. Repetir a operação inicial emite nova URL. O Worker não repete sozinho um stream consumido. URLs podem ser reutilizadas até expirar, e a validade também é limitada pela sessão IAM. Expiração é verificada quando a requisição chega ao S3; uma transferência já iniciada não precisa terminar antes do prazo, mas reconexões podem falhar. [URLs pré-assinadas S3](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html).

Em streaming, Worker cria headers de saída do zero e usa `redirect:manual` em todos os fetches. É essencial: Cloudflare documenta que seguir redirects automaticamente pode encaminhar Authorization e cookies ao novo destino. [Request API](https://developers.cloudflare.com/workers/runtime-apis/request/).

## Entrada HTTP e limites

| Serviço/caminho | Limite relevante | Consequência |
|---|---|---|
| API Gateway HTTP API atual | Payload 10 MB; integração até 30 s | Evento Lambda proxy ainda sujeito a 6 MB. Não serve como entrada de PUT grande para depois decidir redirect. |
| API Gateway REST API | Payload 10 MB; timeout padrão até 29 s, ajustável para Regional/private em condições documentadas | Integração direta ao S3 remove Lambda mas não o limite do Gateway. |
| Lambda Event Function | Invocação síncrona 6 MB; timeout até 900 s | ZIP binário em base64 pode atingir o limite por volta de 4,5 MB menos o envelope, antes de 6 MB de arquivo. Limites exatos dependem da representação. |
| Function URL | Evento HTTP serializado; mesmos limites de entrada da função | Response streaming não é request streaming. Não resolve upload grande. |
| Lambda response streaming | Até 200 MB de resposta; após primeiros 6 MB, banda de resposta limitada a 2 MB/s | Só saída; GET direto S3 continua preferível. |
| ALB com alvo Lambda | Corpo da requisição no máximo 1 MB | Também não é um assinador que recebe PUT grande antes do limite. |
| CloudFront/Edge | Verbos documentados GET/HEAD/OPTIONS/PUT/POST/PATCH/DELETE | Não colocar como entrada DAV completa: faltam PROPFIND/MKCOL; função na borda não demonstra por si só resposta antecipada. |
| Novas Lambda Web Functions | Sete verbos comuns; protocolo half-duplex documentado | PROPFIND/MKCOL rejeitados; resposta só entregue após corpo integral, mesmo com request streaming. Não satisfaz a arquitetura desejada. |
| Cloudflare Worker | 100 MB Free/Pro, 200 MB Business, Enterprise até 5 GB; 128 MB de memória | Streaming evita guardar arquivo em memória, mas não aumenta quota de entrada. Modo redirect não é bypass certificado. |
| S3 PutObject | Uma operação PUT até 5 GB | Não confundir com o máximo maior do multipart. Zotero não gera multipart S3 a partir de redirect. |
| App Runner | Timeout total de requisição de 120 s; memória provisionada cobrada em idle | Pode atrapalhar uploads lentos e não zera custo parado. Métodos/limite da entrada devem ser homologados antes de escolher. |
| ALB → ECS Fargate | Alvo HTTP suporta métodos customizados; não aplicar a ele o limite de 1 MB de alvo Lambda | Alternativa sem EC2 administrada, mas ALB/task têm cobrança contínua. Ajustar idle timeout/streaming e testar limites de ponta a ponta. |

Fontes: [HTTP API quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html), [REST quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-execution-service-limits-table.html), [Lambda quotas](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html), [Function URLs](https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html), [response streaming](https://docs.aws.amazon.com/lambda/latest/dg/configuration-response-streaming.html), [ALB/Lambda](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/lambda-functions.html), [CloudFront](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/RequestAndResponseBehaviorCustomOrigin.html), [Web Functions HTTP](https://docs.aws.amazon.com/lambda/latest/dg/web-functions-http.html), [Web Functions networking](https://docs.aws.amazon.com/lambda/latest/dg/web-functions-networking.html), [Cloudflare limits](https://developers.cloudflare.com/workers/platform/limits/), [S3 upload](https://docs.aws.amazon.com/AmazonS3/latest/userguide/upload-objects.html), [App Runner](https://docs.aws.amazon.com/apprunner/latest/dg/develop.html), [ALB custom methods](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/rule-condition-types.html).

Não confundimos `ANY`/`$default` com comprovação de verbos arbitrários. A REST API documenta os sete métodos comuns; a rota `$default` da HTTP API existente não foi testada remotamente. O transporte interno POST implementado evita depender disso. [Métodos API Gateway](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-method-settings-method-request.html).

## Reaproveitamento e pontos encontrados no Go

- `infra/server_webdav/handler.go:460`: PUT passa `request.Body` ao `s3.PutObject`; não há upload multipart nem gravação temporária explícita. Download em `handleGet` assina GET e retorna 307.
- `infra/handler_webdav/handler.go`, `handlePut` e `requestBody`: evento em string/base64 convertido em `[]byte` e `bytes.Reader`; não pode receber upload arbitrariamente grande.
- Ambos implementam Basic, caminhos, XML DAV, Head/Get/Put/Delete/List e coleção virtual. As novas rotas reaproveitam autorização, resolução de caminhos e handlers de PROPFIND/MKCOL.
- Nenhuma dependência explícita de disco local, lock DAV, mutex de estado da biblioteca ou sessão de cliente no servidor. Clientes AWS/configuração vivem em memória; estado persistente está no S3. Shutdown HTTP e sinais só se aplicam ao servidor contínuo. Não é necessário EFS, DynamoDB ou disco compartilhado para reproduzir o comportamento atual.
- **Pendência preexistente:** `listCollection` faz apenas uma chamada ListObjectsV2, sem paginação, ao contrário de `listKeys`. PROPFIND Depth 1 pode omitir objetos após a primeira página de até 1.000 entradas. Corrigir antes de depender de limpeza de bibliotecas grandes; não foi alterado nesta implementação do upload.
- Listagens e XML completos ficam em memória e podem ultrapassar o limite de resposta Lambda em bibliotecas muito grandes. É uma limitação separada do tamanho de um anexo.
- PUT e `.prop` não formam uma transação entre objetos. Concorrência segue a lógica do Zotero e last-writer-wins do backend. Não introduzir locks em memória para tentar tornar múltiplas instâncias consistentes.
- Terraform do bucket já tinha mudança local para versionamento `Suspended`. Não foi aplicado nem revertido. IAM atual restringe objetos a `zotero/*`, mas lista o bucket todo e inclui GetObjectVersion/GetObjectAttributes não usados no fluxo inspecionado. Antes de produção, restringir ListBucket com condição de prefixo e remover ações não utilizadas; testar 404 vs 403 para objetos ausentes. O novo assinador não requer ampliar permissões.
- `PRESIGNED_PUT_EXPIRATION` existia no Terraform, mas não era usado pelo handler; agora é usado, com padrão 120 s e teto 300 s.

## Preservação dos objetos

Os clientes geram `zotero/ITEMKEY.zip` e `zotero/ITEMKEY.prop`; existe marcador `zotero/`. A resolução de caminhos atual mantém o prefixo configurado. A ponte envia o caminho original ao mesmo resolvedor e não renomeia, extrai, recomprime ou fragmenta ZIPs. O `.prop` continua sendo enviado pelo próprio Zotero e armazenado como bytes opacos.

Não substituir o hash do `.prop` pelo ETag S3: representam conceitos diferentes e ETag pode mudar com multipart/criptografia. Não reescrever mtime a partir de LastModified S3. Preservar nomes e conteúdo dentro dos ZIPs, objetos legados e marcador de coleção. O teste de restauração deve comparar SHA-256 dos bytes do ZIP/PROP e abrir os anexos; listar objetos apenas não basta.

## Custos pessoais estimados

Premissas ilustrativas: região AWS US East (Ohio), 20 GB em S3 Standard, 10.000 chamadas leves/mês, Lambda ARM 128 MB por 100 ms, 2.000 PUT/LIST e 10.000 GET/HEAD S3, 5 GB/mês de downloads para internet. Sem provisioned concurrency, NAT Gateway, cache pago ou recursos novos de ALB. Valores em USD, sem impostos, domínio, câmbio, créditos promocionais, custo de versões antigas ou tráfego extraordinário; confirmar a tabela regional no momento de contratar.

| Item | Cálculo aproximado | Mês |
|---|---|---:|
| S3 Standard | 20 × US$0,023/GB | US$0,46 |
| PUT/LIST | 2.000 × US$0,005/1.000 | US$0,01 |
| GET/HEAD | 10.000 × US$0,0004/1.000 | US$0,004 |
| API Gateway HTTP | 10.000 × US$1/milhão | US$0,01 |
| Lambda ARM | 125 GB-s × ~US$0,0000133334 + 10.000 × US$0,20/milhão | ~US$0,0037 |
| Logs | Dependem de bytes/retention; reservar para uso pequeno | US$0–0,10 |
| Download S3 | 5 GB × US$0,09/GB, conservador sem franquia | US$0,45 |
| Cloudflare Workers Free | Dentro de 100.000 requisições/dia e CPU do plano | US$0 |
| Workers Paid, se escolhido | Assinatura mínima, mesmo sem tráfego | +US$5 |

Total indicativo **US$0,94–1,04/mês** sem franquia de egress; aproximadamente **US$0,49–0,59** se os downloads estiverem dentro da franquia AWS agregada aplicável à conta. A maior parte é armazenamento/tráfego. Não é orçamento baseado no consumo real: não consultamos conta, tamanhos do bucket ou fatura.

Ociosidade: Worker Free + Lambda on-demand + HTTP API não têm processamento reservado cobrado quando não há chamadas. S3 continua cobrando ~US$0,46 para esses 20 GB, além de logs armazenados. Workers Paid mantém US$5/mês; logs existentes e qualquer infraestrutura EC2/NAT/ALB já implantada continuam cobrando até serem desativados em etapa posterior.

Streaming tem tráfego Cloudflare→S3 para uploads, sem egress de download de anexos pelo Worker. S3 recebe uploads sem taxa de entrada padrão; GET/HEAD de metadados e download direto mantêm suas cobranças usuais. O Worker Free tem orçamento de CPU pequeno: medir em homologação, sem assumir que toda carga cabe só porque está abaixo da quota diária.

Fontes de preço: [S3](https://aws.amazon.com/s3/pricing/), [Lambda](https://aws.amazon.com/lambda/pricing/), [API Gateway](https://aws.amazon.com/api-gateway/pricing/), [Cloudflare](https://developers.cloudflare.com/workers/platform/pricing/).

Alternativas:

- **Fargate ARM 0,25 vCPU/0,5 GB, 730 h/mês:** cerca de US$7,21/mês só task (tarifas de referência US East); ALB ~US$16,43/mês + LCU, pelo menos dois IPv4 do ALB ~US$7,30/mês, e eventualmente IPv4 da task ~US$3,65/mês. Total de referência ~US$31–35 antes de S3/logs/DNS. Evitar NAT pago para uso pessoal; considerar desenho de rede e S3 gateway endpoint. [Fargate](https://aws.amazon.com/fargate/pricing/), [ELB](https://aws.amazon.com/elasticloadbalancing/pricing/), [IPv4](https://aws.amazon.com/vpc/pricing/). Escalar task para zero não dá despertar HTTP transparente automaticamente; manter ALB custa mesmo sem tasks.
- **App Runner 0,5 GB:** memória ociosa ~0,5 × 730 × US$0,007 = US$2,56/mês, mais CPU ativa, builds/logs/transferência conforme configuração; timeout 120 s exige cuidado. Não equivale a zero em idle. [Preço](https://aws.amazon.com/apprunner/pricing/).
- **Divisão no cliente:** Zotero original envia um ZIP por anexo, não chunks S3. Plugin/cliente alterado teria de negociar URLs, enviar partes e concluir multipart. Dividir PDF manualmente muda os anexos e referências; colocar partes dentro de um ZIP não reduz o tamanho do PUT. Não satisfaz a preferência por Zotero original e preservação transparente.
- **Proxy serverless externo com quota maior:** possível pesquisa futura, mas precisa suportar DAV, autenticação Basic, entrada streaming, tempos de conexão e custos de egress ao S3. Não assumir compatibilidade a partir de “serverless”.

## Migração e reversão

1. Guardar configuração/artefato atual e um manifesto completo dos objetos, com paginação, tamanho, versão quando existir e hashes obtidos de cópia verificável. Não registrar senhas. Não presumir versionamento ativo: código local mostra Suspended. Planejar backup/versionamento sob autorização posterior, incluindo custos; não fazer limpeza durante a transição.
2. Homologar em bucket e biblioteca descartáveis, com os dois perfis e matriz de testes do README. Não usar “resetar sincronização” na biblioteca real como teste. Validar o formato `.prop` legado e atual.
3. Corrigir/verificar paginação antes de ativar purge em biblioteca com muitas entradas. Medir tamanho do maior ZIP e da listagem XML. Se houver ZIP >100 MB, Free/Pro não atende ao requisito completo.
4. Publicar o código Lambda mantendo endpoint antigo e funções existentes; habilitar segredo; publicar Worker sem mudar ainda clientes. Revisar o plano Terraform para não aplicar outras mudanças locais do bucket.
5. Em janela curta, pausar sincronização automática nos clientes, estabelecer backup consistente e trocar a URL em apenas um cliente. Manter mesmos bucket/prefixo/credenciais. Não é necessário copiar/transformar todos os objetos para adotar o Worker.
6. Validar leitura de anexos antigos; criar um anexo novo, observar ZIP→PROP e baixar em outro cliente; comparar integridade. Só então migrar os demais. Manter versão anterior do Worker e Lambda para rollback.
7. Reversão: pausar sync, voltar URL antiga em todos os clientes e versão anterior/configuração se necessário. Objetos criados pelo Worker continuam no mesmo formato e caminhos. Se o endpoint antigo for API Gateway/Lambda, seu limite de upload grande volta a existir; conservar também o caminho HTTP/container anterior se ele era o que atendia esses arquivos. Não fazer restore cego que apague uploads novos: reconciliar por manifesto/versões.
8. Apenas após aceite e período de observação, autorizar desligamento da EC2/caminho anterior e limpeza de recursos de teste. Nada disso foi executado nesta etapa.
