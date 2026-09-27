-- Exact distinct-order count per day, maintained synchronously on every order
-- insert (see OrderService.createOrder) so a date-range total is a cheap
-- SUM over a handful of rows instead of a COUNT(*) scan over every matching
-- order. Deliberately has NO category/status/region column: daily_summary
-- already covers the category breakdown, but summing per-category rows
-- double-counts any order whose items span more than one category. This
-- table only ever holds one row per day, so it can't double-count anything.
CREATE TABLE "daily_order_count" (
  "date"         date   PRIMARY KEY,
  "totalOrders"  bigint NOT NULL DEFAULT 0
);

INSERT INTO "daily_order_count" ("date", "totalOrders")
SELECT "placedAt"::date, COUNT(*)
FROM "orders"
GROUP BY "placedAt"::date;
