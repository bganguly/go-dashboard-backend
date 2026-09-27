CREATE TABLE "daily_summary" (
  "id"            serial PRIMARY KEY,
  "date"          date NOT NULL,
  "categoryId"    integer NOT NULL,
  "categoryName"  varchar(100) NOT NULL,
  "regionId"      integer NOT NULL,
  "regionCode"    varchar(10) NOT NULL,
  "totalOrders"   integer NOT NULL DEFAULT 0,
  "totalRevenue"  numeric(14,2) NOT NULL DEFAULT 0,
  "totalItems"    integer NOT NULL DEFAULT 0,
  "avgOrderValue" numeric(10,2) NOT NULL DEFAULT 0,
  "createdAt"     timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt"     timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE("date","categoryId","regionId")
);
CREATE INDEX "daily_summary_date_idx"       ON "daily_summary"("date");
CREATE INDEX "daily_summary_categoryId_idx" ON "daily_summary"("categoryId");
CREATE INDEX "daily_summary_regionId_idx"   ON "daily_summary"("regionId");
