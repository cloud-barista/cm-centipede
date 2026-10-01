-- Matrix source seed - MySQL
--
-- Deliberately small and shaped to exercise structure rather than volume:
-- 4 tables with 3 foreign keys, a view, a function, a procedure, a trigger and
-- an event. A version difference shows up in what survives a dump/restore, not
-- in how many rows there were.
--
-- The multibyte rows are the charset check: Korean, Japanese and an emoji, so a
-- collation regression appears in the data rather than only in metadata.
--
-- Loaded by scripts/init-mysql.sh, inside the database it created.

CREATE TABLE customers (
  id         INT AUTO_INCREMENT PRIMARY KEY,
  name       VARCHAR(100) NOT NULL,
  email      VARCHAR(100) NOT NULL UNIQUE,
  grade      ENUM('bronze','silver','gold') NOT NULL DEFAULT 'bronze',
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE products (
  id    INT AUTO_INCREMENT PRIMARY KEY,
  name  VARCHAR(100) NOT NULL,
  price DECIMAL(10,2) NOT NULL,
  stock INT NOT NULL DEFAULT 0
);

CREATE TABLE orders (
  id          INT AUTO_INCREMENT PRIMARY KEY,
  customer_id INT NOT NULL,
  status      VARCHAR(20) NOT NULL DEFAULT 'pending',
  ordered_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_orders_customer FOREIGN KEY (customer_id) REFERENCES customers(id)
);

CREATE TABLE order_items (
  id         INT AUTO_INCREMENT PRIMARY KEY,
  order_id   INT NOT NULL,
  product_id INT NOT NULL,
  qty        INT NOT NULL DEFAULT 1,
  unit_price DECIMAL(10,2) NOT NULL,
  CONSTRAINT fk_items_order   FOREIGN KEY (order_id)   REFERENCES orders(id),
  CONSTRAINT fk_items_product FOREIGN KEY (product_id) REFERENCES products(id)
);

CREATE VIEW v_order_summary AS
  SELECT c.id AS customer_id, c.name AS customer_name,
         COUNT(DISTINCT o.id) AS order_count,
         IFNULL(SUM(oi.qty * oi.unit_price), 0) AS total_amount
    FROM customers c
    LEFT JOIN orders o       ON c.id = o.customer_id
    LEFT JOIN order_items oi ON o.id = oi.order_id
   GROUP BY c.id, c.name;

DELIMITER //
CREATE FUNCTION fn_order_total(p_order_id INT) RETURNS DECIMAL(12,2) READS SQL DATA
BEGIN
  DECLARE total DECIMAL(12,2) DEFAULT 0;
  SELECT IFNULL(SUM(qty * unit_price), 0) INTO total FROM order_items WHERE order_id = p_order_id;
  RETURN total;
END//

CREATE PROCEDURE sp_customer_orders(IN p_customer_id INT)
BEGIN
  SELECT o.id, o.status, fn_order_total(o.id) AS total
    FROM orders o WHERE o.customer_id = p_customer_id ORDER BY o.id;
END//

CREATE TRIGGER trg_order_items_ai AFTER INSERT ON order_items FOR EACH ROW
BEGIN
  UPDATE products SET stock = stock - NEW.qty WHERE id = NEW.product_id;
END//
DELIMITER ;

CREATE EVENT evt_matrix_touch
  ON SCHEDULE EVERY 1 DAY STARTS CURRENT_TIMESTAMP
  DO UPDATE products SET stock = stock WHERE id = 0;

INSERT INTO customers (name, email, grade) VALUES
  ('Alice Kim',   'alice@example.com',   'gold'),
  ('Bob Lee',     'bob@example.com',     'silver'),
  ('Charlie Park','charlie@example.com', 'bronze'),
  ('김철수 (한글)', 'utf8-ko@example.com', 'gold'),
  ('佐藤 太郎 🎌',  'utf8-ja@example.com', 'silver');

INSERT INTO products (name, price, stock) VALUES
  ('Laptop Pro',     1299.99, 50),
  ('Wireless Mouse',   29.99, 200),
  ('USB-C Hub',        49.99, 150),
  ('4K Monitor',      399.99, 30),
  ('노트북 거치대 🖥',   39.99, 120);

INSERT INTO orders (customer_id, status) VALUES
  (1, 'confirmed'), (2, 'shipped'), (3, 'pending'), (4, 'confirmed'), (5, 'shipped');

INSERT INTO order_items (order_id, product_id, qty, unit_price) VALUES
  (1, 1, 1, 1299.99), (1, 2, 2, 29.99), (2, 3, 1, 49.99), (3, 4, 1, 399.99),
  (4, 5, 2, 39.99),   (4, 2, 1, 29.99), (5, 1, 1, 1299.99), (5, 3, 3, 49.99);
