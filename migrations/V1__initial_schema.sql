CREATE TYPE "OrderStatus" AS ENUM (
  'PENDING','CONFIRMED','PROCESSING','SHIPPED','DELIVERED','CANCELLED','REFUNDED'
);

CREATE TABLE "categories" (
  "id"       serial PRIMARY KEY,
  "name"     varchar(100) UNIQUE NOT NULL,
  "slug"     varchar(100) UNIQUE NOT NULL,
  "parentId" integer REFERENCES "categories"("id"),
  "createdAt" timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt" timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE "regions" (
  "id"       serial PRIMARY KEY,
  "code"     varchar(10) UNIQUE NOT NULL,
  "name"     varchar(100) NOT NULL,
  "country"  varchar(100) NOT NULL,
  "timezone" varchar(50) NOT NULL
);

CREATE TABLE "customers" (
  "id"        serial PRIMARY KEY,
  "email"     varchar(255) UNIQUE NOT NULL,
  "firstName" varchar(100) NOT NULL,
  "lastName"  varchar(100) NOT NULL,
  "phone"     varchar(30),
  "regionId"  integer NOT NULL REFERENCES "regions"("id"),
  "createdAt" timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt" timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX "customers_regionId_idx" ON "customers"("regionId");
CREATE INDEX "customers_email_idx"    ON "customers"("email");
CREATE INDEX "customers_lastName_idx" ON "customers"("lastName");

CREATE TABLE "products" (
  "id"          serial PRIMARY KEY,
  "sku"         varchar(100) UNIQUE NOT NULL,
  "name"        varchar(255) NOT NULL,
  "description" text,
  "price"       numeric(10,2) NOT NULL,
  "cost"        numeric(10,2) NOT NULL,
  "stock"       integer NOT NULL DEFAULT 0,
  "categoryId"  integer NOT NULL REFERENCES "categories"("id"),
  "createdAt"   timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt"   timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX "products_categoryId_idx" ON "products"("categoryId");
CREATE INDEX "products_sku_idx"        ON "products"("sku");

CREATE TABLE "orders" (
  "id"         serial PRIMARY KEY,
  "customerId" integer NOT NULL REFERENCES "customers"("id"),
  "regionId"   integer NOT NULL REFERENCES "regions"("id"),
  "status"     "OrderStatus" NOT NULL DEFAULT 'PENDING',
  "total"      numeric(12,2) NOT NULL,
  "currency"   varchar(3) NOT NULL DEFAULT 'USD',
  "notes"      text,
  "placedAt"   timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt"  timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX "orders_customerId_idx"              ON "orders"("customerId");
CREATE INDEX "orders_customerId_placedAt_idx"     ON "orders"("customerId","placedAt");
CREATE INDEX "orders_regionId_idx"                ON "orders"("regionId");
CREATE INDEX "orders_regionId_placedAt_idx"       ON "orders"("regionId","placedAt");
CREATE INDEX "orders_status_idx"                  ON "orders"("status");
CREATE INDEX "orders_status_placedAt_idx"         ON "orders"("status","placedAt");
CREATE INDEX "orders_status_regionId_placedAt_idx" ON "orders"("status","regionId","placedAt");
CREATE INDEX "orders_placedAt_idx"                ON "orders"("placedAt");
CREATE INDEX "orders_total_idx"                   ON "orders"("total");
CREATE INDEX "orders_total_placedAt_idx"          ON "orders"("total","placedAt");

CREATE TABLE "order_items" (
  "id"        serial PRIMARY KEY,
  "orderId"   integer NOT NULL REFERENCES "orders"("id"),
  "productId" integer NOT NULL REFERENCES "products"("id"),
  "quantity"  integer NOT NULL,
  "unitPrice" numeric(10,2) NOT NULL,
  "discount"  numeric(5,2) NOT NULL DEFAULT 0
);
CREATE INDEX "order_items_orderId_idx"   ON "order_items"("orderId");
CREATE INDEX "order_items_productId_idx" ON "order_items"("productId");

CREATE TABLE "search_index" (
  "id"         serial PRIMARY KEY,
  "entityType" varchar(50) NOT NULL,
  "entityId"   integer NOT NULL,
  "content"    text NOT NULL,
  "updatedAt"  timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE("entityType","entityId")
);
CREATE INDEX "search_index_entityType_idx" ON "search_index"("entityType");

CREATE TABLE "sessions" (
  "id"        varchar(128) PRIMARY KEY,
  "userId"    integer NOT NULL,
  "data"      jsonb,
  "expiresAt" timestamp(3) NOT NULL,
  "createdAt" timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt" timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX "sessions_userId_idx"    ON "sessions"("userId");
CREATE INDEX "sessions_expiresAt_idx" ON "sessions"("expiresAt");

CREATE TABLE "audit_log" (
  "id"         serial PRIMARY KEY,
  "entityType" varchar(50) NOT NULL,
  "entityId"   integer NOT NULL,
  "action"     varchar(50) NOT NULL,
  "actorId"    integer,
  "before"     jsonb,
  "after"      jsonb,
  "orderId"    integer REFERENCES "orders"("id"),
  "createdAt"  timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX "audit_log_entityType_entityId_idx" ON "audit_log"("entityType","entityId");
CREATE INDEX "audit_log_actorId_idx"  ON "audit_log"("actorId");
CREATE INDEX "audit_log_createdAt_idx" ON "audit_log"("createdAt");
