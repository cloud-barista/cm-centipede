-- ============================================================
-- shop_db — E-Commerce Test Database
-- Includes: Tables, FK(ALTER), Views, Functions,
--           Procedures, Triggers, Events
-- Engine: MySQL 8.0+
-- ============================================================

-- NOTE: database creation/USE removed — gendata loads into the target DB
--       (tofu output db_name, e.g. testdb) directly. gendata opens the connection
--       with charset=utf8mb4, so the 4-byte UTF-8 sample data still loads.

-- ============================================================
-- TABLES
-- ============================================================

CREATE TABLE IF NOT EXISTS categories (
    category_id   INT          NOT NULL AUTO_INCREMENT,
    name          VARCHAR(100) NOT NULL,
    description   TEXT,
    parent_id     INT          DEFAULT NULL,
    sort_order    INT          DEFAULT 0,
    is_active     TINYINT(1)   DEFAULT 1,
    created_at    DATETIME     DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (category_id),
    KEY idx_parent (parent_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS products (
    product_id      INT            NOT NULL AUTO_INCREMENT,
    category_id     INT            NOT NULL,
    name            VARCHAR(200)   NOT NULL,
    description     TEXT,
    price           DECIMAL(12,2)  NOT NULL,
    stock_quantity  INT            DEFAULT 0,
    sku             VARCHAR(60)    NOT NULL,
    weight_kg       DECIMAL(8,3)   DEFAULT NULL,
    is_active       TINYINT(1)     DEFAULT 1,
    created_at      DATETIME       DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME       DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (product_id),
    UNIQUE KEY uq_sku (sku),
    KEY idx_category (category_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS customers (
    customer_id   INT          NOT NULL AUTO_INCREMENT,
    email         VARCHAR(200) NOT NULL,
    first_name    VARCHAR(100) NOT NULL,
    last_name     VARCHAR(100) NOT NULL,
    phone         VARCHAR(20)  DEFAULT NULL,
    address       TEXT,
    city          VARCHAR(100) DEFAULT NULL,
    country       CHAR(2)      DEFAULT 'KR',
    grade         ENUM('Bronze','Silver','Gold','VIP') DEFAULT 'Bronze',
    is_active     TINYINT(1)   DEFAULT 1,
    created_at    DATETIME     DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME     DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (customer_id),
    UNIQUE KEY uq_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS orders (
    order_id          INT          NOT NULL AUTO_INCREMENT,
    customer_id       INT          NOT NULL,
    status            ENUM('pending','confirmed','shipped','delivered','cancelled','refunded') DEFAULT 'pending',
    total_amount      DECIMAL(12,2) NOT NULL DEFAULT 0.00,
    shipping_address  TEXT,
    tracking_number   VARCHAR(100) DEFAULT NULL,
    notes             TEXT,
    created_at        DATETIME     DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME     DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (order_id),
    KEY idx_customer (customer_id),
    KEY idx_status_created (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS order_items (
    item_id        INT           NOT NULL AUTO_INCREMENT,
    order_id       INT           NOT NULL,
    product_id     INT           NOT NULL,
    quantity       INT           NOT NULL DEFAULT 1,
    unit_price     DECIMAL(12,2) NOT NULL,
    discount_rate  DECIMAL(5,2)  DEFAULT 0.00,
    PRIMARY KEY (item_id),
    KEY idx_order (order_id),
    KEY idx_product (product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS payments (
    payment_id      INT           NOT NULL AUTO_INCREMENT,
    order_id        INT           NOT NULL,
    amount          DECIMAL(12,2) NOT NULL,
    method          ENUM('card','bank_transfer','virtual_account','point') DEFAULT 'card',
    status          ENUM('pending','completed','failed','refunded') DEFAULT 'pending',
    transaction_id  VARCHAR(200)  DEFAULT NULL,
    paid_at         DATETIME      DEFAULT NULL,
    created_at      DATETIME      DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (payment_id),
    KEY idx_order (order_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS reviews (
    review_id   INT          NOT NULL AUTO_INCREMENT,
    product_id  INT          NOT NULL,
    customer_id INT          NOT NULL,
    rating      TINYINT      NOT NULL,
    title       VARCHAR(200) DEFAULT NULL,
    content     TEXT,
    is_verified TINYINT(1)   DEFAULT 0,
    created_at  DATETIME     DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (review_id),
    KEY idx_product (product_id),
    KEY idx_customer (customer_id),
    CONSTRAINT chk_rating CHECK (rating BETWEEN 1 AND 5)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS inventory_log (
    log_id          INT          NOT NULL AUTO_INCREMENT,
    product_id      INT          NOT NULL,
    change_type     ENUM('purchase','sale','adjustment','return') NOT NULL,
    quantity_change INT          NOT NULL,
    quantity_after  INT          NOT NULL,
    reference_id    INT          DEFAULT NULL,
    notes           TEXT,
    created_at      DATETIME     DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (log_id),
    KEY idx_product (product_id),
    KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS coupons (
    coupon_id      INT           NOT NULL AUTO_INCREMENT,
    code           VARCHAR(50)   NOT NULL,
    discount_type  ENUM('rate','fixed') DEFAULT 'rate',
    discount_value DECIMAL(10,2) NOT NULL,
    min_order_amt  DECIMAL(12,2) DEFAULT 0.00,
    max_usage      INT           DEFAULT NULL,
    used_count     INT           DEFAULT 0,
    expires_at     DATETIME      DEFAULT NULL,
    is_active      TINYINT(1)    DEFAULT 1,
    created_at     DATETIME      DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (coupon_id),
    UNIQUE KEY uq_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS wishlist (
    wishlist_id  INT      NOT NULL AUTO_INCREMENT,
    customer_id  INT      NOT NULL,
    product_id   INT      NOT NULL,
    added_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (wishlist_id),
    UNIQUE KEY uq_customer_product (customer_id, product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


-- ============================================================
-- FOREIGN KEYS (ALTER TABLE)
-- ============================================================

ALTER TABLE categories
    ADD CONSTRAINT fk_category_parent
    FOREIGN KEY (parent_id) REFERENCES categories(category_id)
    ON DELETE SET NULL ON UPDATE CASCADE;

ALTER TABLE products
    ADD CONSTRAINT fk_product_category
    FOREIGN KEY (category_id) REFERENCES categories(category_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE orders
    ADD CONSTRAINT fk_order_customer
    FOREIGN KEY (customer_id) REFERENCES customers(customer_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE order_items
    ADD CONSTRAINT fk_item_order
    FOREIGN KEY (order_id) REFERENCES orders(order_id)
    ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE order_items
    ADD CONSTRAINT fk_item_product
    FOREIGN KEY (product_id) REFERENCES products(product_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE payments
    ADD CONSTRAINT fk_payment_order
    FOREIGN KEY (order_id) REFERENCES orders(order_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE reviews
    ADD CONSTRAINT fk_review_product
    FOREIGN KEY (product_id) REFERENCES products(product_id)
    ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE reviews
    ADD CONSTRAINT fk_review_customer
    FOREIGN KEY (customer_id) REFERENCES customers(customer_id)
    ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE inventory_log
    ADD CONSTRAINT fk_invlog_product
    FOREIGN KEY (product_id) REFERENCES products(product_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE wishlist
    ADD CONSTRAINT fk_wishlist_customer
    FOREIGN KEY (customer_id) REFERENCES customers(customer_id)
    ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE wishlist
    ADD CONSTRAINT fk_wishlist_product
    FOREIGN KEY (product_id) REFERENCES products(product_id)
    ON DELETE CASCADE ON UPDATE CASCADE;


-- ============================================================
-- VIEWS  (CREATE OR REPLACE VIEW is supported in MySQL)
-- ============================================================

CREATE OR REPLACE VIEW v_order_summary AS
SELECT
    o.order_id,
    o.status                                                   AS order_status,
    o.total_amount,
    o.created_at                                               AS ordered_at,
    c.customer_id,
    CONCAT(c.first_name, ' ', c.last_name)                    AS customer_name,
    c.email,
    c.grade                                                    AS customer_grade,
    COUNT(oi.item_id)                                          AS item_count,
    SUM(oi.quantity)                                           AS total_qty,
    p.status                                                   AS payment_status,
    p.method                                                   AS payment_method
FROM orders o
JOIN  customers  c  ON o.customer_id = c.customer_id
LEFT  JOIN order_items oi ON o.order_id  = oi.order_id
LEFT  JOIN payments    p  ON o.order_id  = p.order_id
GROUP BY
    o.order_id, o.status, o.total_amount, o.created_at,
    c.customer_id, customer_name, c.email, c.grade,
    p.status, p.method;


CREATE OR REPLACE VIEW v_product_stats AS
SELECT
    p.product_id,
    p.name                                  AS product_name,
    p.sku,
    p.price,
    p.stock_quantity,
    p.is_active,
    cat.name                                AS category_name,
    COUNT(DISTINCT r.review_id)             AS review_count,
    ROUND(AVG(r.rating), 2)                 AS avg_rating,
    COALESCE(SUM(oi.quantity), 0)           AS total_sold,
    COALESCE(SUM(oi.quantity * oi.unit_price
              * (1 - oi.discount_rate/100)), 0) AS total_revenue
FROM products p
JOIN  categories  cat ON p.category_id  = cat.category_id
LEFT  JOIN reviews     r  ON p.product_id   = r.product_id
LEFT  JOIN order_items oi ON p.product_id   = oi.product_id
GROUP BY
    p.product_id, p.name, p.sku, p.price,
    p.stock_quantity, p.is_active, cat.name;


CREATE OR REPLACE VIEW v_customer_stats AS
SELECT
    c.customer_id,
    CONCAT(c.first_name, ' ', c.last_name)  AS full_name,
    c.email,
    c.grade,
    COUNT(DISTINCT o.order_id)              AS total_orders,
    COALESCE(SUM(o.total_amount), 0)        AS total_spent,
    COALESCE(AVG(o.total_amount), 0)        AS avg_order_value,
    MAX(o.created_at)                       AS last_order_at
FROM customers c
LEFT JOIN orders o
    ON c.customer_id = o.customer_id AND o.status NOT IN ('cancelled','refunded')
GROUP BY c.customer_id, full_name, c.email, c.grade;


CREATE OR REPLACE VIEW v_low_stock_alert AS
SELECT
    p.product_id,
    p.name          AS product_name,
    p.sku,
    p.stock_quantity,
    cat.name        AS category_name
FROM products p
JOIN categories cat ON p.category_id = cat.category_id
WHERE p.stock_quantity < 10 AND p.is_active = 1
ORDER BY p.stock_quantity ASC;


-- ============================================================
-- FUNCTIONS
-- ============================================================

DROP FUNCTION IF EXISTS fn_apply_discount;
DROP FUNCTION IF EXISTS fn_customer_grade;
DROP FUNCTION IF EXISTS fn_format_krw;
DROP FUNCTION IF EXISTS fn_calc_discounted_total;

DELIMITER $$

CREATE FUNCTION fn_apply_discount(
    p_price         DECIMAL(12,2),
    p_discount_rate DECIMAL(5,2)
)
RETURNS DECIMAL(12,2)
DETERMINISTIC
BEGIN
    RETURN ROUND(p_price * (1 - p_discount_rate / 100), 2);
END$$


CREATE FUNCTION fn_customer_grade(
    p_total_spent DECIMAL(12,2)
)
RETURNS VARCHAR(10)
DETERMINISTIC
BEGIN
    DECLARE v_grade VARCHAR(10);
    IF    p_total_spent >= 5000000 THEN SET v_grade = 'VIP';
    ELSEIF p_total_spent >= 1000000 THEN SET v_grade = 'Gold';
    ELSEIF p_total_spent >= 300000  THEN SET v_grade = 'Silver';
    ELSE                                 SET v_grade = 'Bronze';
    END IF;
    RETURN v_grade;
END$$


CREATE FUNCTION fn_format_krw(p_amount DECIMAL(12,2))
RETURNS VARCHAR(50)
DETERMINISTIC
BEGIN
    RETURN CONCAT('₩', FORMAT(p_amount, 0));
END$$


CREATE FUNCTION fn_calc_discounted_total(p_order_id INT)
RETURNS DECIMAL(12,2)
READS SQL DATA
BEGIN
    DECLARE v_total DECIMAL(12,2) DEFAULT 0;
    SELECT COALESCE(SUM(fn_apply_discount(unit_price, discount_rate) * quantity), 0)
    INTO   v_total
    FROM   order_items
    WHERE  order_id = p_order_id;
    RETURN v_total;
END$$

DELIMITER ;


-- ============================================================
-- PROCEDURES
-- ============================================================

DROP PROCEDURE IF EXISTS sp_create_order;
DROP PROCEDURE IF EXISTS sp_add_order_item;
DROP PROCEDURE IF EXISTS sp_process_payment;
DROP PROCEDURE IF EXISTS sp_update_customer_grade;
DROP PROCEDURE IF EXISTS sp_monthly_sales_report;

DELIMITER $$

CREATE PROCEDURE sp_create_order(
    IN  p_customer_id       INT,
    IN  p_shipping_address  TEXT,
    IN  p_notes             TEXT,
    OUT p_order_id          INT
)
BEGIN
    DECLARE EXIT HANDLER FOR SQLEXCEPTION
    BEGIN
        ROLLBACK;
        RESIGNAL;
    END;

    START TRANSACTION;
    INSERT INTO orders (customer_id, status, shipping_address, notes)
    VALUES (p_customer_id, 'pending', p_shipping_address, p_notes);
    SET p_order_id = LAST_INSERT_ID();
    COMMIT;
END$$


CREATE PROCEDURE sp_add_order_item(
    IN p_order_id      INT,
    IN p_product_id    INT,
    IN p_quantity      INT,
    IN p_discount_rate DECIMAL(5,2)
)
BEGIN
    DECLARE v_price DECIMAL(12,2);
    DECLARE v_stock INT;

    DECLARE EXIT HANDLER FOR SQLEXCEPTION
    BEGIN
        ROLLBACK;
        RESIGNAL;
    END;

    START TRANSACTION;

    SELECT price, stock_quantity
    INTO   v_price, v_stock
    FROM   products
    WHERE  product_id = p_product_id FOR UPDATE;

    IF v_stock < p_quantity THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'Insufficient stock';
    END IF;

    INSERT INTO order_items (order_id, product_id, quantity, unit_price, discount_rate)
    VALUES (p_order_id, p_product_id, p_quantity, v_price, p_discount_rate);

    UPDATE orders
    SET total_amount = total_amount
                     + fn_apply_discount(v_price, p_discount_rate) * p_quantity
    WHERE order_id = p_order_id;

    COMMIT;
END$$


CREATE PROCEDURE sp_process_payment(
    IN p_order_id       INT,
    IN p_method         VARCHAR(20),
    IN p_transaction_id VARCHAR(200)
)
BEGIN
    DECLARE v_amount DECIMAL(12,2);

    SELECT total_amount INTO v_amount
    FROM   orders WHERE order_id = p_order_id;

    INSERT INTO payments (order_id, amount, method, status, transaction_id, paid_at)
    VALUES (p_order_id, v_amount, p_method, 'completed', p_transaction_id, NOW());

    UPDATE orders
    SET    status = 'confirmed', updated_at = NOW()
    WHERE  order_id = p_order_id;
END$$


CREATE PROCEDURE sp_update_customer_grade(IN p_customer_id INT)
BEGIN
    DECLARE v_total_spent DECIMAL(12,2);
    DECLARE v_grade       VARCHAR(10);

    SELECT COALESCE(SUM(total_amount), 0)
    INTO   v_total_spent
    FROM   orders
    WHERE  customer_id = p_customer_id AND status = 'delivered';

    SET v_grade = fn_customer_grade(v_total_spent);

    UPDATE customers
    SET grade = v_grade, updated_at = NOW()
    WHERE customer_id = p_customer_id;
END$$


CREATE PROCEDURE sp_monthly_sales_report(
    IN p_year  INT,
    IN p_month INT
)
BEGIN
    SELECT
        DATE_FORMAT(o.created_at, '%Y-%m')  AS period,
        COUNT(DISTINCT o.order_id)           AS order_count,
        SUM(o.total_amount)                  AS total_revenue,
        COUNT(DISTINCT o.customer_id)        AS unique_customers,
        ROUND(AVG(o.total_amount), 2)        AS avg_order_value,
        SUM(oi.quantity)                     AS total_items_sold
    FROM  orders o
    JOIN  order_items oi ON o.order_id = oi.order_id
    WHERE YEAR(o.created_at)  = p_year
      AND MONTH(o.created_at) = p_month
      AND o.status NOT IN ('cancelled', 'refunded')
    GROUP BY DATE_FORMAT(o.created_at, '%Y-%m');
END$$

DELIMITER ;


-- ============================================================
-- TRIGGERS
-- ============================================================

DROP TRIGGER IF EXISTS trg_after_order_item_insert;
DROP TRIGGER IF EXISTS trg_before_product_update;
DROP TRIGGER IF EXISTS trg_after_payment_update;
DROP TRIGGER IF EXISTS trg_before_review_insert;
DROP TRIGGER IF EXISTS trg_after_order_update;

DELIMITER $$

-- On order item insert: decrease stock and write an inventory log
CREATE TRIGGER trg_after_order_item_insert
AFTER INSERT ON order_items
FOR EACH ROW
BEGIN
    UPDATE products
    SET    stock_quantity = stock_quantity - NEW.quantity
    WHERE  product_id = NEW.product_id;

    INSERT INTO inventory_log
        (product_id, change_type, quantity_change, quantity_after, reference_id, notes)
    SELECT NEW.product_id,
           'sale',
           -NEW.quantity,
           stock_quantity,
           NEW.order_id,
           CONCAT('Order #', NEW.order_id)
    FROM   products
    WHERE  product_id = NEW.product_id;
END$$


-- Guard so stock can never go negative
CREATE TRIGGER trg_before_product_update
BEFORE UPDATE ON products
FOR EACH ROW
BEGIN
    IF NEW.stock_quantity < 0 THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'Stock quantity cannot be negative';
    END IF;
END$$


-- Sync the order status automatically when the payment status changes
CREATE TRIGGER trg_after_payment_update
AFTER UPDATE ON payments
FOR EACH ROW
BEGIN
    IF NEW.status = 'completed' AND OLD.status <> 'completed' THEN
        UPDATE orders
        SET    status = 'confirmed', updated_at = NOW()
        WHERE  order_id = NEW.order_id AND status = 'pending';
    END IF;

    IF NEW.status = 'refunded' AND OLD.status <> 'refunded' THEN
        UPDATE orders
        SET    status = 'refunded', updated_at = NOW()
        WHERE  order_id = NEW.order_id;
    END IF;
END$$


-- Validate the rating range before insert and reset is_verified
CREATE TRIGGER trg_before_review_insert
BEFORE INSERT ON reviews
FOR EACH ROW
BEGIN
    IF NEW.rating < 1 OR NEW.rating > 5 THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'Rating must be between 1 and 5';
    END IF;
    SET NEW.is_verified = 0;
END$$


-- On order cancel or refund: restore stock and write an inventory log
CREATE TRIGGER trg_after_order_update
AFTER UPDATE ON orders
FOR EACH ROW
BEGIN
    IF NEW.status IN ('cancelled', 'refunded')
       AND OLD.status NOT IN ('cancelled', 'refunded')
    THEN
        UPDATE products p
        JOIN   order_items oi ON p.product_id = oi.product_id
        SET    p.stock_quantity = p.stock_quantity + oi.quantity
        WHERE  oi.order_id = NEW.order_id;

        INSERT INTO inventory_log
            (product_id, change_type, quantity_change, quantity_after, reference_id, notes)
        SELECT oi.product_id,
               'return',
               oi.quantity,
               p.stock_quantity,
               NEW.order_id,
               CONCAT('Order #', NEW.order_id, ' ', NEW.status)
        FROM   order_items oi
        JOIN   products p ON oi.product_id = p.product_id
        WHERE  oi.order_id = NEW.order_id;
    END IF;
END$$

DELIMITER ;


-- ============================================================
-- EVENTS
-- ============================================================

-- NOTE: `SET GLOBAL event_scheduler = ON` removed — not permitted on managed DBs
--       (RDS: needs SUPER/SYSTEM_VARIABLES_ADMIN). The events below are still
--       defined, just not auto-scheduled; test data does not need them to fire.

DROP EVENT IF EXISTS evt_cleanup_inventory_logs;
DROP EVENT IF EXISTS evt_auto_cancel_stale_orders;
DROP EVENT IF EXISTS evt_recalculate_customer_grades;

DELIMITER $$

-- Purge inventory logs older than 90 days (daily at 02:00)
CREATE EVENT evt_cleanup_inventory_logs
ON SCHEDULE EVERY 1 DAY
STARTS (CURRENT_DATE + INTERVAL 1 DAY + INTERVAL 2 HOUR)
DO
BEGIN
    DELETE FROM inventory_log
    WHERE created_at < DATE_SUB(NOW(), INTERVAL 90 DAY);
END$$


-- Auto-cancel pending orders older than 24 hours (hourly)
CREATE EVENT evt_auto_cancel_stale_orders
ON SCHEDULE EVERY 1 HOUR
STARTS CURRENT_TIMESTAMP
DO
BEGIN
    UPDATE orders
    SET    status = 'cancelled', updated_at = NOW()
    WHERE  status = 'pending'
      AND  created_at < DATE_SUB(NOW(), INTERVAL 24 HOUR);
END$$


-- Daily at midnight: recompute every customer grade
CREATE EVENT evt_recalculate_customer_grades
ON SCHEDULE EVERY 1 DAY
STARTS (CURRENT_DATE + INTERVAL 1 DAY)
DO
BEGIN
    DECLARE v_cid INT;
    DECLARE done  INT DEFAULT FALSE;
    DECLARE cur CURSOR FOR SELECT customer_id FROM customers WHERE is_active = 1;
    DECLARE CONTINUE HANDLER FOR NOT FOUND SET done = TRUE;

    OPEN cur;
    grade_loop: LOOP
        FETCH cur INTO v_cid;
        IF done THEN LEAVE grade_loop; END IF;
        CALL sp_update_customer_grade(v_cid);
    END LOOP;
    CLOSE cur;
END$$

DELIMITER ;


-- ============================================================
-- SAMPLE DATA
-- ============================================================

-- categories (2-level hierarchy)
INSERT INTO categories (name, description, parent_id, sort_order) VALUES
('Electronics', 'Electronic devices and accessories',       NULL, 1),
('Apparel',     'Apparel for men, women and children',      NULL, 2),
('Books',       'Domestic and imported professional books', NULL, 3),
('Food',        'Fresh and processed food',                 NULL, 4),
('Smartphones', 'Smartphones and tablets',                  1,    1),
('Laptops',     'Laptops and desktops',                     1,    2),
('Peripherals', 'Keyboards, mice and monitors',             1,    3),
('Menswear',    'Menswear specialty section',               2,    1),
('Womenswear',  'Womenswear specialty section',             2,    2),
('IT Books',    'Programming and IT books',                 3,    1);

-- products
INSERT INTO products (category_id, name, description, price, stock_quantity, sku, weight_kg) VALUES
( 5, 'Galaxy S24 Ultra',     'Samsung Galaxy S24 Ultra 256GB',         1599000.00,  50, 'SAMS24U-256', 0.232),
( 5, 'iPhone 15 Pro',        'Apple iPhone 15 Pro 128GB',              1550000.00,  30, 'APIP15P-128', 0.187),
( 5, 'Pixel 8 Pro',          'Google Pixel 8 Pro 128GB',               1199000.00,  20, 'GOGP8P-128',  0.213),
( 6, 'MacBook Pro M3 14"',   'Apple MacBook Pro M3 14-inch 512GB',     2490000.00,  15, 'APMB-M3-14',  1.610),
( 6, 'LG Gram 17',           'LG Gram 17-inch 2024 model',             1690000.00,  25, 'LGGR17-2024', 1.350),
( 7, 'Logitech MX Master 3', 'Logitech MX Master 3S wireless mouse',    139000.00, 100, 'LGMXM3S',     0.141),
( 7, 'Apple Magic Keyboard', 'Apple Magic Keyboard with Touch ID',      179000.00,  80, 'APMK-TID',    0.243),
( 8, 'Basic Casual Shirt',   'Basic 100 percent cotton shirt for men',   39000.00, 200, 'SHIRT-M-BAS', 0.200),
( 9, 'Floral Dress',         'Spring and summer floral pattern dress',   59000.00, 150, 'DRESS-W-FL',  0.280),
(10, 'Clean Code',           'Robert C. Martin - Clean Code',            33000.00, 100, 'BOOK-CLEAN',  0.540),
(10, 'Go Programming',       'The complete guide to the Go language',    38000.00,  80, 'BOOK-GOPR',   0.620),
( 4, 'Organic Apples 5kg',   'Domestic organic apple gift set',          25000.00, 300, 'FOOD-APPLE5', 5.000);

-- customers
INSERT INTO customers (email, first_name, last_name, phone, address, city, grade) VALUES
('kim.jiwon@example.com',    'Jiwon',    'Kim',  '010-1234-5678', '123 Teheran-ro, Gangnam-gu, Seoul',     'Seoul',   'VIP'),
('lee.minjun@example.com',   'Minjun',   'Lee',  '010-2345-6789', '456 Dalmaji-gil, Haeundae-gu, Busan',   'Busan',   'Gold'),
('park.soyeon@example.com',  'Soyeon',   'Park', '010-3456-7890', '789 Dongdaegu-ro, Suseong-gu, Daegu',   'Daegu',   'Silver'),
('choi.hyunwoo@example.com', 'Hyunwoo',  'Choi', '010-4567-8901', '101 Songdo-daero, Yeonsu-gu, Incheon',  'Incheon', 'Bronze'),
('jung.yuna@example.com',    'Yuna',     'Jung', '010-5678-9012', '202 Cheomdangwagi-ro, Buk-gu, Gwangju', 'Gwangju', 'Silver'),
('han.seungmin@example.com', 'Seungmin', 'Han',  '010-6789-0123', '303 Daehak-ro, Yuseong-gu, Daejeon',    'Daejeon', 'Bronze'),
('oh.jieun@example.com',     'Jieun',    'Oh',   '010-7890-1234', '404 Hongik-ro, Mapo-gu, Seoul',         'Seoul',   'Gold'),
('lim.taehyun@example.com',  'Taehyun',  'Lim',  '010-8901-2345', '505 Olympic-ro, Songpa-gu, Seoul',      'Seoul',   'Bronze');

-- coupons
INSERT INTO coupons (code, discount_type, discount_value, min_order_amt, max_usage, expires_at) VALUES
('WELCOME10', 'rate',  10.00,       0.00, 1000, '2025-12-31 23:59:59'),
('SAVE50000', 'fixed', 50000.00, 300000.00,  500, '2025-06-30 23:59:59'),
('VIP20',     'rate',  20.00,  500000.00,  100, '2025-12-31 23:59:59'),
('SUMMER15',  'rate',  15.00,  100000.00,  200, '2025-08-31 23:59:59');

-- orders (direct INSERT with explicit status and amount, placed before
-- order_items so the stock trigger does not fire on them)
INSERT INTO orders (customer_id, status, total_amount, shipping_address, tracking_number) VALUES
(1, 'delivered', 1599000.00, '123 Teheran-ro, Gangnam-gu, Seoul',     'TRACK-20240101-001'),
(2, 'shipped',   2490000.00, '456 Dalmaji-gil, Haeundae-gu, Busan',   'TRACK-20240102-001'),
(3, 'confirmed',   96000.00, '789 Dongdaegu-ro, Suseong-gu, Daegu',   NULL),
(4, 'pending',   1199000.00, '101 Songdo-daero, Yeonsu-gu, Incheon',  NULL),
(5, 'delivered', 1690000.00, '202 Cheomdangwagi-ro, Buk-gu, Gwangju', 'TRACK-20240105-001'),
(1, 'delivered',   71000.00, '123 Teheran-ro, Gangnam-gu, Seoul',     'TRACK-20240106-001'),
(6, 'cancelled',   39000.00, '303 Daehak-ro, Yuseong-gu, Daejeon',    NULL),
(7, 'refunded',    59000.00, '404 Hongik-ro, Mapo-gu, Seoul',         'TRACK-20240108-001'),
(8, 'confirmed',  317000.00, '505 Olympic-ro, Songpa-gu, Seoul',      NULL),
(2, 'delivered',  139000.00, '456 Dalmaji-gil, Haeundae-gu, Busan',   'TRACK-20240110-001');

-- initial inventory purchase logs (the order_items INSERT trigger adds sale logs on top)
INSERT INTO inventory_log (product_id, change_type, quantity_change, quantity_after, notes) VALUES
( 1, 'purchase',  50,  50, 'Initial stock-in'),
( 2, 'purchase',  30,  30, 'Initial stock-in'),
( 3, 'purchase',  20,  20, 'Initial stock-in'),
( 4, 'purchase',  15,  15, 'Initial stock-in'),
( 5, 'purchase',  25,  25, 'Initial stock-in'),
( 6, 'purchase', 100, 100, 'Initial stock-in'),
( 7, 'purchase',  80,  80, 'Initial stock-in'),
( 8, 'purchase', 200, 200, 'Initial stock-in'),
( 9, 'purchase', 150, 150, 'Initial stock-in'),
(10, 'purchase', 100, 100, 'Initial stock-in'),
(11, 'purchase',  80,  80, 'Initial stock-in'),
(12, 'purchase', 300, 300, 'Initial stock-in');

-- order_items (trg_after_order_item_insert handles the stock decrease and the sale log)
INSERT INTO order_items (order_id, product_id, quantity, unit_price, discount_rate) VALUES
(1,  1, 1, 1599000.00, 0.00),
(2,  4, 1, 2490000.00, 0.00),
(3, 10, 1,   33000.00, 5.00),
(3, 11, 1,   38000.00, 5.00),
(3, 12, 1,   25000.00, 0.00),
(4,  3, 1, 1199000.00, 0.00),
(5,  5, 1, 1690000.00, 0.00),
(6, 10, 1,   33000.00, 5.00),
(6, 11, 1,   38000.00, 5.00),
(7,  8, 1,   39000.00, 0.00),
(8,  9, 1,   59000.00, 0.00),
(9,  6, 1,  139000.00, 0.00),
(9,  7, 1,  179000.00, 0.00),
(9, 12, 2,   25000.00, 5.00),
(10, 6, 1,  139000.00, 0.00);

-- payments
INSERT INTO payments (order_id, amount, method, status, transaction_id, paid_at) VALUES
(1, 1599000.00, 'card',            'completed', 'TXN-2024-001', '2024-01-01 10:30:00'),
(2, 2490000.00, 'bank_transfer',   'completed', 'TXN-2024-002', '2024-01-02 14:20:00'),
(3,   96000.00, 'card',            'completed', 'TXN-2024-003', '2024-01-03 09:15:00'),
(5, 1690000.00, 'card',            'completed', 'TXN-2024-005', '2024-01-05 16:45:00'),
(6,   71000.00, 'point',           'completed', 'TXN-2024-006', '2024-01-06 11:30:00'),
(8,   59000.00, 'card',            'refunded',  'TXN-2024-008', '2024-01-08 13:00:00'),
(9,  317000.00, 'virtual_account', 'completed', 'TXN-2024-009', '2024-01-09 12:00:00'),
(10, 139000.00, 'card',            'completed', 'TXN-2024-010', '2024-01-10 10:00:00');

-- reviews
INSERT INTO reviews (product_id, customer_id, rating, title, content, is_verified) VALUES
( 1, 1, 5, 'The best Galaxy yet',               'Both the S Pen and the camera are excellent. Highly recommended!', 1),
( 4, 2, 5, 'Overwhelming M3 performance',       'The M3 chip is astonishingly fast and the battery lasts all day.', 1),
( 5, 5, 4, 'Light and practical',               'Very light for a 17-inch. Perfect for work!',                      1),
(10, 1, 5, 'A must-read for developers',        'It changed the way I write code.',                                 1),
(11, 6, 4, 'Ideal for getting started with Go', 'Strongly recommended for anyone new to Go.',                       0),
( 3, 3, 3, 'Decent value for money',            'Plenty of features, but the camera is a little disappointing.',    1),
( 6, 8, 5, 'The ultimate mouse',                'Ergonomic design that is easy on the wrist.',                      1),
( 9, 7, 4, 'Pretty and comfortable',            'Nice fabric and a flattering fit, so I wear it often.',            1);

-- wishlist
INSERT INTO wishlist (customer_id, product_id) VALUES
(1, 4), (1, 7),
(2, 1), (2, 6),
(3, 4), (3, 5),
(4, 1), (4, 2),
(5, 6), (5, 11),
(6, 4), (7, 5);

-- ============================================================
-- UTF-8 ENCODING VERIFICATION ROWS
--
-- Every other row in this file is ASCII. These rows are deliberately
-- multibyte (Korean, Japanese, emoji) so a migration that loses the
-- charset or collation shows up as mojibake instead of passing silently.
-- ============================================================

INSERT INTO categories (name, description, parent_id, sort_order) VALUES
('한글 카테고리',  '한글 인코딩 검증용 카테고리 - 가나다라마바사', NULL, 98),
('日本語カテゴリ', '日本語エンコーディング検証 - あいうえお',      NULL, 99);

INSERT INTO customers (email, first_name, last_name, phone, address, city, grade) VALUES
('utf8.ko@example.com',    '지은',  '홍',   '010-0000-0001', '서울 종로구 세종대로 1', '서울',   'Bronze'),
('utf8.emoji@example.com', 'Emoji', 'Test', '010-0000-0002', 'Migration ok ✅ 🚀 🐛',  'Global', 'Bronze');
