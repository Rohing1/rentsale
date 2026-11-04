# RentSale API 与验收流程

所有金额均为“分”的整数；日期统一为 `YYYY-MM-DD`。若启用了 `APP_API_KEY`，请求还需添加 `X-API-Key` 请求头。

创建订单和库存调整还必须携带一个针对本次业务操作唯一的 `Idempotency-Key` 请求头。网络超时后以相同的键重试，会返回原操作结果而不会重复变更库存。

## 创建商品

`POST /api/products`

```json
{
  "sku": "CAM-001",
  "name": "全画幅相机",
  "product_type": "equipment",
  "category": "摄影器材",
  "location": "徐汇门店",
  "rent_enabled": true,
  "sell_enabled": true,
  "daily_rent_cents": 12000,
  "sale_price_cents": 399900,
  "deposit_cents": 200000,
  "stock_on_hand": 3
}
```

## 创建客户

`POST /api/customers`

```json
{
  "full_name": "示例客户",
  "phone": "13800000000",
  "identity_ref": "证件脱敏引用"
}
```

不要将身份证原件、银行卡号或支付凭证原文写入业务库；`identity_ref` 仅用于合规的脱敏引用或第三方 KYC token。

## 创建租赁订单

`POST /api/orders`

请求头：`Idempotency-Key: <unique-key-for-this-order>`

```json
{
  "order_type": "rental",
  "customer_id": 1,
  "start_date": "2026-08-10",
  "end_date": "2026-08-13",
  "items": [
    {"product_id": 1, "quantity": 1}
  ],
  "notes": "到店自提"
}
```

租赁订单创建后状态为 `confirmed`。可用状态流转：

```text
confirmed → checked_out → returned
confirmed → cancelled
```

取货只允许在起租日（含）到归还日（不含）内进行；系统会重新锁定商品和复核库存承诺。

## 创建销售订单

`POST /api/orders`

请求头：`Idempotency-Key: <unique-key-for-this-order>`

```json
{
  "order_type": "sale",
  "customer_id": 1,
  "items": [
    {"product_id": 1, "quantity": 1}
  ]
}
```

销售订单创建时状态为 `confirmed` 且立即扣减现货。可用状态流转：

```text
confirmed → completed
confirmed → cancelled
```

取消销售订单只适用于未完成交付的订单，并会恢复库存。

## 查询租期可用量

`GET /api/products/1/availability?start_date=2026-08-10&end_date=2026-08-13`

返回中的 `available` 是展示值。真正的库存判定只发生在创建订单的数据库事务内；如有并发抢占，创建接口会返回 `409 Conflict`，并附带商品 ID 与当时可用数量。

## 更新订单状态

`POST /api/orders/{id}/status`

```json
{
  "status": "checked_out",
  "note": "已核验配件并完成交接"
}
```

`status` 可传 `checked_out`、`returned`、`completed` 或 `cancelled`，但服务会按订单类型和当前状态校验合法流转。

## 登记收款或退款

`POST /api/orders/{id}/payments`

```json
{
  "direction": "charge",
  "amount_cents": 12000,
  "reference": "第三方支付流水号",
  "note": "租金收款"
}
```

退款时将 `direction` 改为 `refund`。`reference` 为必填，且同一订单内不可重复；以相同参考号重试会返回既有流水。服务禁止超额收款与超额退款。

## 调整库存

`POST /api/products/{id}/stock-adjustments`

请求头：`Idempotency-Key: <unique-key-for-this-adjustment>`

```json
{
  "delta": 5,
  "reason": "盘点入库"
}
```

## 部署验收清单

1. 新数据库启动两次，确认两次迁移均成功且 `schema_migrations` 只记录一次每个版本。
2. 创建库存为 1 的可租商品；同时提交两笔同租期订单，必须仅一笔成功，另一笔返回 `409`。
3. 为该商品创建未来租赁确认单后，尝试销售超过“现货减未来峰值租赁需求”的数量，必须返回 `409`。
4. 取消未收款销售单，确认商品库存与 `inventory_movements` 中的恢复流水一致。
5. 租赁单在起租日前取货、归还日后取货都应被拒绝；归还后租期可用量应恢复。
6. 对同一订单创建 / 库存调整请求使用相同 `Idempotency-Key` 重试，确认只产生一份业务记录；对同一订单使用相同支付参考号重试，确认只产生一笔支付流水。
7. 尝试重复 SKU、重复手机号、重复商品订单行、非法状态流转、超额退款，均应得到明确的 4xx 错误且不留下半成品数据。
8. 设置 `APP_API_KEY` 后，未带或带错 `X-API-Key` 的 `/api/*` 请求必须得到 `401`；`/healthz` 可正常访问。
