"""Independent v1 state fixture: two accounts, a partial fill, then a cancel.

Regenerate deliberately with Python 3; review the versioned snapshot contract
before changing field order, union encoding, or nullable response fields.
"""
import json
from pathlib import Path

run = '11111111-1111-4111-8111-111111111111'

def order(order_id, side, price, original, executed, open_qty, cancelled,
          quote, status, reason, created, updated):
    return dict(clientOrderId=order_id, symbol='BTC-USDT', side=side,
                priceTicks=str(price), originalQty=str(original),
                executedQty=str(executed), openQty=str(open_qty),
                cancelledQty=str(cancelled), executedQuoteAtoms=str(quote),
                status=status, reason=reason, createdSeq=str(created), updatedSeq=str(updated))

def place(account, order_id, side, price, quantity):
    return dict(accountId=account, kind='PLACE_ORDER',
                place=dict(clientOrderId=order_id, symbol='BTC-USDT', side=side,
                           type='LIMIT', timeInForce='GTC', priceTicks=str(price), quantityLots=str(quantity)))

def response(account, kind, request, sequence, code, state, trades):
    return dict(schemaVersion=1, runId=run, accountId=account, kind=kind,
                requestId=request, commandSeq=str(sequence), code=code,
                order=state, tradeCount=str(trades))

buy = order('buy', 'BUY', 110, 1, 1, 0, 0, 100, 'FILLED', None, 2, 2)
sell = order('sell', 'SELL', 100, 2, 1, 0, 1, 100, 'CANCELLED', 'USER_CANCEL', 1, 3)
initial_sell = order('sell', 'SELL', 100, 2, 0, 2, 0, 0, 'OPEN', None, 1, 1)
state = dict(
    accounts=[dict(accountId='a', availableBaseLots='1', reservedBaseLots='0', availableQuoteAtoms='900', reservedQuoteAtoms='0'),
              dict(accountId='b', availableBaseLots='4', reservedBaseLots='0', availableQuoteAtoms='100', reservedQuoteAtoms='0')],
    orders=[dict(accountId='a', order=buy), dict(accountId='b', order=sell)],
    book=dict(bids=[], asks=[]),
    trades=[dict(tradeId=run+':2:1', commandSeq='2', matchIndex='1',
                 maker=dict(accountId='b', clientOrderId='sell'), taker=dict(accountId='a', clientOrderId='buy'),
                 takerSide='BUY', priceTicks='100', quantityLots='1')],
    results=[dict(command=place('a', 'buy', 'BUY', 110, 1), response=response('a', 'PLACE_ORDER', 'buy', 2, 'ACCEPTED', buy, 1)),
             dict(command=dict(accountId='b', kind='CANCEL_ORDER', cancel=dict(cancelRequestId='cancel-sell', clientOrderId='sell')),
                  response=response('b', 'CANCEL_ORDER', 'cancel-sell', 3, 'CANCELLED', sell, 0)),
             dict(command=place('b', 'sell', 'SELL', 100, 2), response=response('b', 'PLACE_ORDER', 'sell', 1, 'ACCEPTED', initial_sell, 0))])
Path(__file__).with_name('checkpoint-v1.json').write_bytes(json.dumps(state, separators=(',', ':')).encode('ascii'))
