import http from 'k6/http';
import { check, sleep } from 'k6';

const reversalResponse = http.expectedStatuses(200, 201, 409);

function generateUUID() {
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function(c) {
        let r = Math.random() * 16 | 0, v = c == 'x' ? r : (r & 0x3 | 0x8);
        return v.toString(16);
    });
}

// D-1 avoids UTC/America-Fortaleza day-boundary mismatches.
function safeBusinessDate() {
    const d = new Date();
    d.setUTCDate(d.getUTCDate() - 1);
    return d.toISOString().split('T')[0];
}

export const options = {
    scenarios: {
        write_entries: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: '10s', target: 50 },
                { duration: '30s', target: 50 },
                { duration: '10s', target: 0 },
            ],
            exec: 'writeEntries',
        },
        read_balances: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: '10s', target: 50 },
                { duration: '30s', target: 50 },
                { duration: '10s', target: 0 },
            ],
            exec: 'readBalances',
        },
        list_entries: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: '10s', target: 50 },
                { duration: '30s', target: 50 },
                { duration: '10s', target: 0 },
            ],
            exec: 'listEntries',
        },
        get_entry: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: '10s', target: 50 },
                { duration: '30s', target: 50 },
                { duration: '10s', target: 0 },
            ],
            exec: 'getEntry',
        },
        reverse_entry: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: '10s', target: 50 },
                { duration: '30s', target: 50 },
                { duration: '10s', target: 0 },
            ],
            exec: 'reverseEntry',
        },
    },
    thresholds: {
        http_req_duration: ['p(95)<1000'],
        http_req_failed: ['rate<0.01'],
    },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const KEYCLOAK_URL = __ENV.KEYCLOAK_URL || 'http://localhost:8080/realms/cashflow/protocol/openid-connect/token';
const CLIENT_ID = __ENV.CLIENT_ID || 'cashflow-merchant-app';
const USERNAME = __ENV.USERNAME || 'merchant-a';
const PASSWORD = __ENV.PASSWORD || 'merchant-a-pass';

export function setup() {
    // Autenticação inicial (Setup é executado apenas 1x antes da carga)
    const res = http.post(KEYCLOAK_URL, {
        grant_type: 'password',
        client_id: CLIENT_ID,
        username: USERNAME,
        password: PASSWORD,
        scope: 'ledger:write ledger:read consolidation:read',
    }, {
        headers: {
            'Host': 'edge.cashflow.local',
            'X-Forwarded-Proto': 'https',
        }
    });
    
    let token = '';
    let entryId = '';
    if (res.status === 200) {
        token = res.json('access_token');
        
        // Seed inicial para garantir que a data exista antes da carga
        const idempotencyKey = generateUUID();
        const entryPayload = JSON.stringify({
            type: 'ENTRY_TYPE_CREDIT',
            amount: '1000',
            currency: 'BRL',
            businessDate: safeBusinessDate(),
            description: 'Seed entry'
        });
        
        let seedRes = http.post(`${BASE_URL}/v1/entries`, entryPayload, {
            headers: {
                'Content-Type': 'application/json',
                'Authorization': `Bearer ${token}`,
                'Idempotency-Key': idempotencyKey,
                'Host': 'edge.cashflow.local',
                'X-Forwarded-Proto': 'https',
            }
        });
        
        if (seedRes.status === 200 || seedRes.status === 201) {
            entryId = seedRes.json('entry.entryId') || seedRes.json('entryId');
        }
        
        // Aguarda um pouco para o consumidor processar
        sleep(1);
    } else {
        console.warn(`Falha na autenticação. Status: ${res.status}. Body: ${res.body}`);
    }
    
    return { token, entryId };
}

export function writeEntries(data) {
    const idempotencyKey = generateUUID();
    const entryPayload = JSON.stringify({
        type: 'ENTRY_TYPE_CREDIT',
        amount: '1000',
        currency: 'BRL',
        businessDate: safeBusinessDate(),
        description: 'Load test entry'
    });

    const writeParams = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${data.token}`,
            'Idempotency-Key': idempotencyKey,
            'Host': 'edge.cashflow.local',
            'X-Forwarded-Proto': 'https',
        },
    };

    let writeRes = http.post(`${BASE_URL}/v1/entries`, entryPayload, writeParams);
    check(writeRes, {
        'write status is 200 or 201': (r) => r.status === 200 || r.status === 201,
    });
    sleep(1);
}

export function readBalances(data) {
    const readParams = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${data.token}`,
            'Host': 'edge.cashflow.local',
            'X-Forwarded-Proto': 'https',
        },
    };

    const today = safeBusinessDate();
    let readRes = http.get(`${BASE_URL}/v1/daily-balances?start_date=${today}&end_date=${today}`, readParams);
    check(readRes, {
        'read status is 200': (r) => r.status === 200,
    });
    sleep(1);
}

export function listEntries(data) {
    const readParams = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${data.token}`,
            'Host': 'edge.cashflow.local',
            'X-Forwarded-Proto': 'https',
        },
    };

    let listRes = http.get(`${BASE_URL}/v1/entries?page_size=10`, readParams);
    check(listRes, {
        'list status is 200': (r) => r.status === 200,
    });
    sleep(1);
}

export function getEntry(data) {
    if (!data.entryId) { sleep(1); return; }
    
    const readParams = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${data.token}`,
            'Host': 'edge.cashflow.local',
            'X-Forwarded-Proto': 'https',
        },
    };

    let getRes = http.get(`${BASE_URL}/v1/entries/${data.entryId}`, readParams);
    check(getRes, {
        'get entry status is 200': (r) => r.status === 200,
    });
    sleep(1);
}

export function reverseEntry(data) {
    if (!data.entryId) { sleep(1); return; }
    
    const idempotencyKey = generateUUID();
    const writeParams = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${data.token}`,
            'Idempotency-Key': idempotencyKey,
            'Host': 'edge.cashflow.local',
            'X-Forwarded-Proto': 'https',
        },
    };

    let revRes = http.post(`${BASE_URL}/v1/entries/${data.entryId}/reversals`, "{}", {
        ...writeParams,
        responseCallback: reversalResponse,
    });
    check(revRes, {
        'reverse status is valid (200/201/409)': (r) => r.status === 200 || r.status === 201 || r.status === 409,
    });
    sleep(1);
}
