import http from 'k6/http';
import { check, sleep } from 'k6';

// Teste de carga via Edge (KrakenD): simula 100 usuarios concorrentes, cada
// um executando a jornada completa por TODAS as rotas publicas da API --
// nao so escrita e leitura isoladas. Por rodar atras do KrakenD, este teste
// tambem esta sujeito ao rate limit por IP do gateway (client_max_rate em
// deploy/edge/krakend/krakend.json); como todos os VUs do k6 saem do mesmo
// container/IP, ele mede "o gateway aguenta 100 usuarios reais" e nao a
// capacidade do dominio isolado -- para isso, ver load-backend.js e o
// RNF-03 em docs/testing-traceability.md.
function generateUUID() {
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function (c) {
        const r = (Math.random() * 16) | 0;
        const v = c === 'x' ? r : (r & 0x3) | 0x8;
        return v.toString(16);
    });
}

export const options = {
    scenarios: {
        full_journey: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: '15s', target: 100 }, // rampa de subida ate 100 usuarios
                { duration: '40s', target: 100 }, // sustentacao com 100 usuarios concorrentes
                { duration: '10s', target: 0 },   // rampa de descida
            ],
            exec: 'userJourney',
        },
    },
    thresholds: {
        // 95% das requisicoes devem responder em menos de 15s (ajustado para o
        // enfileiramento do Docker Desktop local, nao um SLO de producao).
        http_req_duration: ['p(95)<15000'],
        http_req_failed: ['rate<0.05'],
        'checks{step:create}': ['rate>0.95'],
        'checks{step:get}': ['rate>0.95'],
        'checks{step:list}': ['rate>0.95'],
        'checks{step:reverse}': ['rate>0.95'],
        'checks{step:balances}': ['rate>0.95'],
    },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const KEYCLOAK_URL = __ENV.KEYCLOAK_URL || 'http://localhost:8080/realms/cashflow/protocol/openid-connect/token';
const CLIENT_ID = __ENV.CLIENT_ID || 'cashflow-merchant-app';
const USERNAME = __ENV.USERNAME || 'merchant-a';
const PASSWORD = __ENV.PASSWORD || 'merchant-a-pass';

function authHeaders(token, extra) {
    return {
        headers: Object.assign(
            {
                'Content-Type': 'application/json',
                Authorization: `Bearer ${token}`,
                Host: 'edge.cashflow.local',
                'X-Forwarded-Proto': 'https',
            },
            extra || {}
        ),
    };
}

export function setup() {
    // Autenticacao acontece uma vez, fora da carga medida.
    const res = http.post(
        KEYCLOAK_URL,
        {
            grant_type: 'password',
            client_id: CLIENT_ID,
            username: USERNAME,
            password: PASSWORD,
            scope: 'ledger:write ledger:read consolidation:read',
        },
        { headers: { Host: 'edge.cashflow.local', 'X-Forwarded-Proto': 'https' } }
    );

    if (res.status !== 200) {
        throw new Error(`Falha na autenticacao antes da carga. Status: ${res.status}. Body: ${res.body}`);
    }

    return { token: res.json('access_token') };
}

// userJourney exercita as 5 rotas publicas do sistema em sequencia, na
// ordem que um comerciante real usaria: cria um lancamento, confere que
// foi gravado, lista seus lancamentos, confere o saldo diario consolidado
// e por fim estorna o que criou -- fechando o ciclo sem inflar o saldo a
// cada iteracao/VU.
export function userJourney(data) {
    const today = new Date().toISOString().split('T')[0];

    const createPayload = JSON.stringify({
        type: 'ENTRY_TYPE_CREDIT',
        amount: '1000',
        currency: 'BRL',
        businessDate: today,
        description: 'Load test entry',
    });
    const createRes = http.post(
        `${BASE_URL}/v1/entries`,
        createPayload,
        authHeaders(data.token, { 'Idempotency-Key': generateUUID() })
    );
    const created = check(createRes, { 'create entry: status is 200': (r) => r.status === 200 }, { step: 'create' });
    if (!created) {
        sleep(1);
        return;
    }
    const entryId = createRes.json('entry.entryId') || createRes.json('entryId');

    const getRes = http.get(`${BASE_URL}/v1/entries/${entryId}`, authHeaders(data.token));
    check(getRes, { 'get entry: status is 200': (r) => r.status === 200 }, { step: 'get' });

    const listRes = http.get(`${BASE_URL}/v1/entries?page_size=20`, authHeaders(data.token));
    check(listRes, { 'list entries: status is 200': (r) => r.status === 200 }, { step: 'list' });

    const balancesRes = http.get(
        `${BASE_URL}/v1/daily-balances?start_date=${today}&end_date=${today}`,
        authHeaders(data.token)
    );
    check(balancesRes, { 'daily balances: status is 200': (r) => r.status === 200 }, { step: 'balances' });

    const reverseRes = http.post(
        `${BASE_URL}/v1/entries/${entryId}/reversals`,
        null,
        authHeaders(data.token, { 'Idempotency-Key': generateUUID() })
    );
    check(reverseRes, { 'reverse entry: status is 200': (r) => r.status === 200 }, { step: 'reverse' });

    sleep(1);
}
