-- Matrix source seed - PostgreSQL
--
-- Deliberately small and shaped to exercise structure rather than volume:
-- 4 tables with 3 foreign keys, a view, two functions (one of them a trigger
-- function), a procedure and a trigger. PostgreSQL has no event scheduler, so
-- the MySQL seed's event has no counterpart here.
--
-- The multibyte rows are the charset check: Korean, Japanese and an emoji.
--
-- Loaded by scripts/init-postgresql.sh, inside the database it created.

CREATE TABLE customers (
  id         SERIAL PRIMARY KEY,
  name       VARCHAR(100) NOT NULL,
  email      VARCHAR(100) NOT NULL UNIQUE,
  grade      VARCHAR(10)  NOT NULL DEFAULT 'bronze',
  created_at TIMESTAMP    DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE products (
  id    SERIAL PRIMARY KEY,
  name  VARCHAR(100)  NOT NULL,
  price NUMERIC(10,2) NOT NULL,
  stock INT           NOT NULL DEFAULT 0
);

CREATE TABLE orders (
  id          SERIAL PRIMARY KEY,
  customer_id INT         NOT NULL REFERENCES customers(id),
  status      VARCHAR(20) NOT NULL DEFAULT 'pending',
  ordered_at  TIMESTAMP   DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE order_items (
  id         SERIAL PRIMARY KEY,
  order_id   INT           NOT NULL REFERENCES orders(id),
  product_id INT           NOT NULL REFERENCES products(id),
  qty        INT           NOT NULL DEFAULT 1,
  unit_price NUMERIC(10,2) NOT NULL
);

CREATE VIEW v_order_summary AS
  SELECT c.id AS customer_id, c.name AS customer_name,
         COUNT(DISTINCT o.id) AS order_count,
         COALESCE(SUM(oi.qty * oi.unit_price), 0) AS total_amount
    FROM customers c
    LEFT JOIN orders o       ON c.id = o.customer_id
    LEFT JOIN order_items oi ON o.id = oi.order_id
   GROUP BY c.id, c.name;

CREATE FUNCTION fn_order_total(p_order_id INT) RETURNS NUMERIC(12,2) AS $$
DECLARE total NUMERIC(12,2);
BEGIN
  SELECT COALESCE(SUM(qty * unit_price), 0) INTO total FROM order_items WHERE order_id = p_order_id;
  RETURN total;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION fn_order_items_ai() RETURNS TRIGGER AS $$
BEGIN
  UPDATE products SET stock = stock - NEW.qty WHERE id = NEW.product_id;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_order_items_ai AFTER INSERT ON order_items
  FOR EACH ROW EXECUTE FUNCTION fn_order_items_ai();

CREATE PROCEDURE sp_touch_products() LANGUAGE plpgsql AS $$
BEGIN
  UPDATE products SET stock = stock WHERE id = 0;
END;
$$;

INSERT INTO customers (name, email, grade) VALUES
  ('Alice Kim',    'alice@example.com',   'gold'),
  ('Bob Lee',      'bob@example.com',     'silver'),
  ('Charlie Park', 'charlie@example.com', 'bronze'),
  ('김철수 (한글)',  'utf8-ko@example.com', 'gold'),
  ('佐藤 太郎 🎌',   'utf8-ja@example.com', 'silver');

INSERT INTO products (name, price, stock) VALUES
  ('Laptop Pro',      1299.99, 50),
  ('Wireless Mouse',    29.99, 200),
  ('USB-C Hub',         49.99, 150),
  ('4K Monitor',       399.99, 30),
  ('노트북 거치대 🖥',    39.99, 120);

INSERT INTO orders (customer_id, status) VALUES
  (1, 'confirmed'), (2, 'shipped'), (3, 'pending'), (4, 'confirmed'), (5, 'shipped');

INSERT INTO order_items (order_id, product_id, qty, unit_price) VALUES
  (1, 1, 1, 1299.99), (1, 2, 2, 29.99), (2, 3, 1, 49.99), (3, 4, 1, 399.99),
  (4, 5, 2, 39.99),   (4, 2, 1, 29.99), (5, 1, 1, 1299.99), (5, 3, 3, 49.99);
