// Fixup T11 (rnf03-rate-limit-vs-capacity): load.js measures the KrakenD
// edge's per-IP rate limit (qos/ratelimit/router, client_max_rate in
// deploy/edge/krakend/krakend.json), not the Ledger/Consolidation
// domain's own capacity, because every k6 VU shares the same source IP.
// This script proves (or disproves) the second, distinct claim RNF-03
// actually makes: that the domain itself sustains the 50 RPS peak profile
// within SLO, by talking to ledger-api/consolidation-api directly inside
// the Docker network, bypassing the Edge and its rate limit entirely.
//
// Token issuance still goes through Keycloak (BACKEND_BASE_URL only
// bypasses KrakenD for the Ledger/Consolidation calls themselves), so this
// still measures real authenticated, authorized traffic, not an
// unauthenticated backdoor.
import http from 'k6/http';
import { check, sleep } from 'k6';

function generateUUID() {
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function(c) {
        let r = Math.random() * 16 | 0, v = c == 'x' ? r : (r & 0x3 | 0x8);
        return v.toString(16);
    });
}

export const options = {
    stages: [
        { duration: '10s', target: 50 },
        { duration: '30s', target: 50 },
        { duration: '10s', target: 0 },
    ],
    thresholds: {
        http_req_duration: ['p(95)<200'],
        http_req_failed: ['rate<0.01'],
    },
};

// Separate base URLs: writes hit the Ledger's own public HTTP listener,
// reads hit Consolidation's, both reachable directly on the Docker
// network's internal service DNS, never through KrakenD.
const LEDGER_URL = __ENV.LEDGER_URL || 'http://ledger-api:8081';
const CONSOLIDATION_URL = __ENV.CONSOLIDATION_URL || 'http://consolidation-api:8082';
const KEYCLOAK_URL = __ENV.KEYCLOAK_URL || 'https://keycloak:8443/realms/cashflow/protocol/openid-connect/token';
const CLIENT_ID = __ENV.CLIENT_ID || 'cashflow-merchant-app';
const USERNAME = __ENV.USERNAME || 'merchant-a';
const PASSWORD = __ENV.PASSWORD || 'merchant-a-pass';

export function setup() {
    const res = http.post(KEYCLOAK_URL, {
        grant_type: 'password',
        client_id: CLIENT_ID,
        username: USERNAME,
        password: PASSWORD,
        scope: 'ledger:write ledger:read consolidation:read',
    }, {
        headers: { 'Host': 'edge.cashflow.local' },
    });

    let token = '';
    if (res.status === 200) {
        token = res.json('access_token');

        const idempotencyKey = generateUUID();
        const entryPayload = JSON.stringify({
            type: 'ENTRY_TYPE_CREDIT',
            amount: '1000',
            currency: 'BRL',
            businessDate: new Date().toISOString().split('T')[0],
            description: 'Seed entry (backend-direct)',
        });
        http.post(`${LEDGER_URL}/v1/entries`, entryPayload, {
            headers: {
                'Content-Type': 'application/json',
                'Authorization': `Bearer ${token}`,
                'Idempotency-Key': idempotencyKey,
            },
        });
        sleep(1);
    } else {
        console.warn(`Falha na autenticação. Status: ${res.status}. Body: ${res.body}`);
    }

    return { token };
}

export default function (data) {
    const idempotencyKey = generateUUID();
    const entryPayload = JSON.stringify({
        type: 'ENTRY_TYPE_CREDIT',
        amount: '1000',
        currency: 'BRL',
        businessDate: new Date().toISOString().split('T')[0],
        description: 'Load test entry (backend-direct)',
    });

    const writeParams = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${data.token}`,
            'Idempotency-Key': idempotencyKey,
        },
    };

    let writeRes = http.post(`${LEDGER_URL}/v1/entries`, entryPayload, writeParams);
    if (writeRes.status !== 200 && __ITER === 0) {
        console.error(`Write failed! Status: ${writeRes.status}, Body: ${writeRes.body}`);
    }
    check(writeRes, { 'write status is 200': (r) => r.status === 200 });

    const readParams = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${data.token}`,
        },
    };
    const today = new Date().toISOString().split('T')[0];
    let readRes = http.get(`${CONSOLIDATION_URL}/v1/daily-balances?start_date=${today}&end_date=${today}`, readParams);
    if (readRes.status !== 200 && __ITER === 0) {
        console.error(`Read failed! Status: ${readRes.status}, Body: ${readRes.body}`);
    }
    check(readRes, { 'read status is 200': (r) => r.status === 200 });

    sleep(1);
}
