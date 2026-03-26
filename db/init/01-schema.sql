create table if not exists products (
    sku text primary key,
    name text not null,
    description text not null,
    category text not null,
    price numeric(10, 2) not null,
    updated_at timestamptz not null default now()
);

create table if not exists stock_levels (
    sku text not null references products (sku),
    warehouse_id integer not null,
    quantity integer not null,
    reserved integer not null default 0,
    updated_at timestamptz not null default now(),
    primary key (sku, warehouse_id)
);

create table if not exists orders (
    order_ref text primary key,
    user_id text not null,
    sku text not null references products (sku),
    total numeric(10, 2) not null,
    status text not null,
    created_at timestamptz not null default now()
);

create table if not exists payment_attempts (
    id bigserial primary key,
    order_ref text not null unique,
    amount numeric(10, 2) not null,
    status text not null,
    created_at timestamptz not null default now()
);

create index if not exists idx_products_category on products (category);
create index if not exists idx_stock_levels_sku on stock_levels (sku);
create index if not exists idx_orders_user_created on orders (user_id, created_at desc);

insert into products (sku, name, description, category, price, updated_at)
select
    format('sku-%s', gs),
    format('Product %s', gs),
    case
        when gs % 4 = 0 then format('Trail running shoe %s with grippy outsole and weather guard', gs)
        when gs % 4 = 1 then format('Urban commuter backpack %s with laptop sleeve and bottle pocket', gs)
        when gs % 4 = 2 then format('Office chair %s with mesh back, lumbar support, and adjustable height', gs)
        else format('Training top %s with moisture management and reflective trim', gs)
    end,
    (array['trail', 'travel', 'office', 'fitness'])[(gs % 4) + 1],
    round((25 + (gs % 60))::numeric + 0.95, 2),
    now() - make_interval(hours => gs)
from generate_series(1, 180) as gs
on conflict (sku) do nothing;

insert into stock_levels (sku, warehouse_id, quantity, reserved, updated_at)
select
    format('sku-%s', sku_id),
    warehouse_id,
    20 + ((sku_id + warehouse_id) % 50),
    (sku_id + warehouse_id) % 4,
    now() - make_interval(hours => warehouse_id)
from generate_series(1, 180) as sku_id
cross join generate_series(1, 6) as warehouse_id
on conflict (sku, warehouse_id) do nothing;

insert into orders (order_ref, user_id, sku, total, status, created_at)
select
    format('ord-%s', gs),
    format('user-%s', ((gs - 1) % 12) + 1),
    format('sku-%s', ((gs - 1) % 180) + 1),
    round((30 + (gs % 70))::numeric + 0.49, 2),
    (array['paid', 'packed', 'shipped', 'delivered'])[(gs % 4) + 1],
    now() - make_interval(hours => gs / 2)
from generate_series(1, 320) as gs
on conflict (order_ref) do nothing;

insert into payment_attempts (order_ref, amount, status, created_at)
select
    order_ref,
    total,
    'captured',
    created_at + interval '5 minutes'
from orders
where order_ref not in (select order_ref from payment_attempts)
limit 200;

