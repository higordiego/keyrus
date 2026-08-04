# ADR-011: Gateway (KrakenD) como Borda Única de Entrada

## O Problema
Se os aplicativos consumissem a API da Ledger, a API do Consolidado e a autenticação do Keycloak de forma independente e separada, a superfície de ataque ficaria imensa. As chamadas maliciosas atingiriam nossa infraestrutura central. Rate Limits, segurança SSL e validações de CORS teriam que ser escritas isoladamente dentro de cada microserviço, ferindo a lógica do negócio (DRY).

## A Decisão
Nenhum microserviço desta plataforma possui rotas públicas expostas para a internet. 

A única camada que possui IP público e responde a requisições é o **KrakenD API Gateway** (Camada L7). Ele opera de forma *stateless* e performática. 
É responsabilidade estrita do KrakenD:
1. Validar e rejeitar tokens adulterados, com assinatura incorreta ou sem escopo antes da requisição tocar nos nossos bancos de dados.
2. Converter rotas JSON em gRPC (se for o caso) e repassá-las internamente.
3. Represar abusos limitando o fluxo de dados por usuário (Rate Limit Local).

## Consequências Positivas
* Segurança: Se o código da Ledger tiver uma vulnerabilidade grave, hackers ainda precisariam invadir o KrakenD antes. O que é bastante complexo.
* Custo: O Gateway barra milhares de requisições maliciosas na borda, economizando recursos computacionais.
* Desempenho e Facilidade: Escrever regras de infraestrutura fica concentrado no arquivo `krakend.json`, mantendo os microsserviços puros (somente com regras financeiras).

## Consequências Negativas
* Single Point of Failure (Ponto Único de Falha). O KrakenD não salva estado, mas precisa escalar horizontalmente; se todos os KrakenDs caírem, a plataforma inteira cai.
* Configurar o arquivo `krakend.json` no começo gera sobrecarga e demanda testes de configuração extras na esteira da CI.

## Gatilhos de Revisão
* Caso a empresa escolha migrar inteiramente para um serviço Kubernetes e Istio/Envoy, o KrakenD poderá ser substituído pela Service Mesh da nuvem hospedeira. Contudo, em ambientes híbridos, o KrakenD segue a melhor pedida em desempenho Open-Source.
