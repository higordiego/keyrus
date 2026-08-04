import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
    // Rampa de carga para simular o "perfil de pico"
    stages: [
        { duration: '10s', target: 50 },  // Rampa de subida rápida
        { duration: '30s', target: 50 },  // Sustentação
        { duration: '10s', target: 0 },   // Rampa de descida
    ],
    thresholds: {
        // 95% das requisições devem ser respondidas em menos de 200ms
        http_req_duration: ['p(95)<200'], 
        // A taxa de erro não pode passar de 1%
        http_req_failed: ['rate<0.01'],   
    },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const KEYCLOAK_URL = __ENV.KEYCLOAK_URL || 'http://localhost:8080/realms/cashflow/protocol/openid-connect/token';
const CLIENT_ID = __ENV.CLIENT_ID || 'cashflow-consolidation-svc';
const CLIENT_SECRET = __ENV.CLIENT_SECRET || 'consolidation-secret-123';

export function setup() {
    // Autenticação inicial (Setup é executado apenas 1x antes da carga)
    const res = http.post(KEYCLOAK_URL, {
        grant_type: 'client_credentials',
        client_id: CLIENT_ID,
        client_secret: CLIENT_SECRET,
    });
    
    let token = '';
    if (res.status === 200) {
        token = res.json('access_token');
    } else {
        console.warn(`Falha na autenticação. Status: ${res.status}. Certifique-se que o Keycloak está rodando e as credenciais são válidas.`);
    }
    
    return { token };
}

export default function (data) {
    const params = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${data.token}`,
        },
    };

    // Consulta do consolidado diário (Fluxo de Leitura)
    let res = http.get(`${BASE_URL}/v1/daily-balances?date=2023-10-01`, params);
    
    check(res, {
        'status is 200 or 404': (r) => r.status === 200 || r.status === 404,
    });

    sleep(1);
}
