#!/usr/bin/env python3
"""Authenticated smoke against running Compose images; creates only UUID test wallets."""
import json
import os
import subprocess
import urllib.error
import urllib.parse
import urllib.request
import uuid
from decimal import Decimal
from pathlib import Path


def request(base, method, path, token=None, body=None, key=None, expected=200):
    data = None if body is None else json.dumps(body).encode()
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if key:
        headers['Idempotency-Key'] = key
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        res = urllib.request.urlopen(req, timeout=15)
    except urllib.error.HTTPError as error:
        res = error
    with res:
        raw = res.read()
        assert res.status == expected, (method, path, res.status, expected)
        return json.loads(raw)


def token(identity):
    issuer = os.getenv('OIDC_ISSUER', 'http://localhost:58080/realms/jungle')
    data = urllib.parse.urlencode({'grant_type': 'client_credentials', 'client_id': identity,
                                  'client_secret': identity + '-local-secret'}).encode()
    with urllib.request.urlopen(issuer + '/protocol/openid-connect/token', data, timeout=10) as res:
        return json.load(res)['access_token']


def money(value):
    return {'amount': str(value), 'currency': 'BRL'}


def main():
    os.chdir(Path(__file__).resolve().parent.parent)
    bases = [os.getenv('SMOKE_BASE')] if os.getenv('SMOKE_BASE') else [
        'http://' + subprocess.check_output(['docker', 'compose', 'port', '--index', str(i), 'app', '8080'], text=True).strip()
        for i in (1, 2, 3)]
    for base in bases:
        request(base, 'GET', '/health/live')
        request(base, 'GET', '/health/ready')
    internal, provider, other = (token(i) for i in ('wallet-internal', 'provider-a', 'provider-b'))
    player = str(uuid.uuid4())
    wallet = request(bases[0], 'POST', '/wallets', internal,
                     {'playerId': player, 'initialBalance': money('100')}, expected=201)['id']
    request(bases[0], 'POST', '/wallets', provider,
            {'playerId': str(uuid.uuid4()), 'initialBalance': money('100')}, expected=403)

    def operation(kind, amount, reference=None):
        op = {'providerId': 'provider-a', 'externalTransactionId': str(uuid.uuid4()),
              'playerId': player, 'walletId': wallet, 'roundId': 'smoke-round', 'gameId': 'smoke-game',
              'kind': kind, 'money': money(amount)}
        if reference:
            op['referenceExternalTransactionId'] = reference
        return op

    def submit(op, key=None, index=0):
        return request(bases[index % len(bases)], 'POST', '/wagering/transactions', provider,
                       op, key or str(uuid.uuid4()))

    bet, key = operation('BET', '25'), str(uuid.uuid4())
    result = submit(bet, key)
    assert result['balance']['amount'] == '75.00'
    request(bases[0], 'GET', '/wagering/transactions/' + result['transactionId'], other, expected=404)
    submit(operation('WIN', '10'), index=1)
    replay = submit(bet, key, index=2)
    assert replay['idempotentReplay'] is True and replay['balance']['amount'] == '75.00'
    refund = operation('REFUND', '25', bet['externalTransactionId'])
    submit(refund, index=1)
    submit(operation('ROLLBACK', '25', refund['externalTransactionId']), index=2)
    submit(operation('LOSS', '0'))
    entries, cursor = [], ''
    while True:
        page = request(bases[0], 'GET', '/wallets/' + wallet + '/ledger?limit=2&cursor=' + cursor, internal)
        entries.extend(page['entries'])
        cursor = page.get('nextCursor')
        if not cursor:
            break
    assert len(entries) == 5 and len({e['id'] for e in entries}) == 5
    calculated = sum((Decimal(e['money']['amount']) * (1 if e['direction'] == 'CREDIT' else -1)
                      for e in entries), Decimal(0))
    assert calculated == Decimal('85.00')
    for base in bases:
        balance = request(base, 'GET', '/wallets/' + wallet, internal)
        assert balance['balance']['amount'] == '85.00' and balance['version'] == 5
        rec = request(base, 'POST', '/wallets/' + wallet + '/reconciliation', internal)
        assert rec['consistent'] is True and rec['checkedEntries'] == 5
    print(json.dumps({'status': 'passed', 'replicas': len(bases), 'walletId': wallet,
                      'balance': '85.00', 'ledgerEntries': 5, 'checks':
                      ['health', 'OIDC', 'authorization', 'BET', 'WIN', 'REFUND', 'ROLLBACK', 'LOSS',
                       'historical replay', 'pagination', 'reconciliation']}, indent=2))


if __name__ == '__main__':
    main()
