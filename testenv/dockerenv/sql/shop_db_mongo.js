// ============================================================
// shop_db — E-Commerce Test Database (MongoDB 7.0)
// mongosh < shop_db_mongo.js
// ============================================================

db = db.getSiblingDB('shop_db');

// ── Drop existing collections ──────────────────────────────
['categories','products','customers','orders','order_items',
 'payments','reviews','inventory_log','coupons','wishlist']
    .forEach(name => { try { db[name].drop(); } catch(e) {} });

// ── categories ─────────────────────────────────────────────
db.categories.insertMany([
    { _id: 1,  name: 'Electronics', description: 'Electronic devices and accessories',       parent_id: null, sort_order: 1, is_active: true, created_at: new Date() },
    { _id: 2,  name: 'Apparel',     description: 'Apparel for men, women and children',      parent_id: null, sort_order: 2, is_active: true, created_at: new Date() },
    { _id: 3,  name: 'Books',       description: 'Domestic and imported professional books', parent_id: null, sort_order: 3, is_active: true, created_at: new Date() },
    { _id: 4,  name: 'Food',        description: 'Fresh and processed food',                 parent_id: null, sort_order: 4, is_active: true, created_at: new Date() },
    { _id: 5,  name: 'Smartphones', description: 'Smartphones and tablets',                  parent_id: 1,    sort_order: 1, is_active: true, created_at: new Date() },
    { _id: 6,  name: 'Laptops',     description: 'Laptops and desktops',                     parent_id: 1,    sort_order: 2, is_active: true, created_at: new Date() },
    { _id: 7,  name: 'Peripherals', description: 'Keyboards, mice and monitors',             parent_id: 1,    sort_order: 3, is_active: true, created_at: new Date() },
    { _id: 8,  name: 'Menswear',    description: 'Menswear specialty section',               parent_id: 2,    sort_order: 1, is_active: true, created_at: new Date() },
    { _id: 9,  name: 'Womenswear',  description: 'Womenswear specialty section',             parent_id: 2,    sort_order: 2, is_active: true, created_at: new Date() },
    { _id: 10, name: 'IT Books',    description: 'Programming and IT books',                 parent_id: 3,    sort_order: 1, is_active: true, created_at: new Date() },
]);

// ── products ───────────────────────────────────────────────
db.products.insertMany([
    { _id: 1,  category_id: 5,  name: 'Galaxy S24 Ultra',     description: 'Samsung Galaxy S24 Ultra 256GB',         price: 1599000, stock_quantity: 50,  sku: 'SAMS24U-256', weight_kg: 0.232, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 2,  category_id: 5,  name: 'iPhone 15 Pro',        description: 'Apple iPhone 15 Pro 128GB',              price: 1550000, stock_quantity: 30,  sku: 'APIP15P-128', weight_kg: 0.187, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 3,  category_id: 5,  name: 'Pixel 8 Pro',          description: 'Google Pixel 8 Pro 128GB',               price: 1199000, stock_quantity: 20,  sku: 'GOGP8P-128',  weight_kg: 0.213, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 4,  category_id: 6,  name: 'MacBook Pro M3 14"',   description: 'Apple MacBook Pro M3 14-inch 512GB',     price: 2490000, stock_quantity: 15,  sku: 'APMB-M3-14',  weight_kg: 1.610, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 5,  category_id: 6,  name: 'LG Gram 17',           description: 'LG Gram 17-inch 2024 model',             price: 1690000, stock_quantity: 25,  sku: 'LGGR17-2024', weight_kg: 1.350, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 6,  category_id: 7,  name: 'Logitech MX Master 3', description: 'Logitech MX Master 3S wireless mouse',   price: 139000,  stock_quantity: 100, sku: 'LGMXM3S',     weight_kg: 0.141, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 7,  category_id: 7,  name: 'Apple Magic Keyboard', description: 'Apple Magic Keyboard with Touch ID',     price: 179000,  stock_quantity: 80,  sku: 'APMK-TID',    weight_kg: 0.243, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 8,  category_id: 8,  name: 'Basic Casual Shirt',   description: 'Basic 100 percent cotton shirt for men', price: 39000,   stock_quantity: 200, sku: 'SHIRT-M-BAS', weight_kg: 0.200, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 9,  category_id: 9,  name: 'Floral Dress',         description: 'Spring and summer floral pattern dress', price: 59000,   stock_quantity: 150, sku: 'DRESS-W-FL',  weight_kg: 0.280, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 10, category_id: 10, name: 'Clean Code',           description: 'Robert C. Martin - Clean Code',          price: 33000,   stock_quantity: 100, sku: 'BOOK-CLEAN',  weight_kg: 0.540, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 11, category_id: 10, name: 'Go Programming',       description: 'The complete guide to the Go language',  price: 38000,   stock_quantity: 80,  sku: 'BOOK-GOPR',   weight_kg: 0.620, is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 12, category_id: 4,  name: 'Organic Apples 5kg',   description: 'Domestic organic apple gift set',        price: 25000,   stock_quantity: 300, sku: 'FOOD-APPLE5', weight_kg: 5.000, is_active: true, created_at: new Date(), updated_at: new Date() },
]);

// ── customers ──────────────────────────────────────────────
db.customers.insertMany([
    { _id: 1, email: 'kim.jiwon@example.com',    first_name: 'Jiwon',    last_name: 'Kim',  phone: '010-1234-5678', address: '123 Teheran-ro, Gangnam-gu, Seoul',     city: 'Seoul',   country: 'KR', grade: 'VIP',    is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 2, email: 'lee.minjun@example.com',   first_name: 'Minjun',   last_name: 'Lee',  phone: '010-2345-6789', address: '456 Dalmaji-gil, Haeundae-gu, Busan',   city: 'Busan',   country: 'KR', grade: 'Gold',   is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 3, email: 'park.soyeon@example.com',  first_name: 'Soyeon',   last_name: 'Park', phone: '010-3456-7890', address: '789 Dongdaegu-ro, Suseong-gu, Daegu',   city: 'Daegu',   country: 'KR', grade: 'Silver', is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 4, email: 'choi.hyunwoo@example.com', first_name: 'Hyunwoo',  last_name: 'Choi', phone: '010-4567-8901', address: '101 Songdo-daero, Yeonsu-gu, Incheon',  city: 'Incheon', country: 'KR', grade: 'Bronze', is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 5, email: 'jung.yuna@example.com',    first_name: 'Yuna',     last_name: 'Jung', phone: '010-5678-9012', address: '202 Cheomdangwagi-ro, Buk-gu, Gwangju', city: 'Gwangju', country: 'KR', grade: 'Silver', is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 6, email: 'han.seungmin@example.com', first_name: 'Seungmin', last_name: 'Han',  phone: '010-6789-0123', address: '303 Daehak-ro, Yuseong-gu, Daejeon',    city: 'Daejeon', country: 'KR', grade: 'Bronze', is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 7, email: 'oh.jieun@example.com',     first_name: 'Jieun',    last_name: 'Oh',   phone: '010-7890-1234', address: '404 Hongik-ro, Mapo-gu, Seoul',         city: 'Seoul',   country: 'KR', grade: 'Gold',   is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 8, email: 'lim.taehyun@example.com',  first_name: 'Taehyun',  last_name: 'Lim',  phone: '010-8901-2345', address: '505 Olympic-ro, Songpa-gu, Seoul',      city: 'Seoul',   country: 'KR', grade: 'Bronze', is_active: true, created_at: new Date(), updated_at: new Date() },
]);

// ── coupons ────────────────────────────────────────────────
db.coupons.insertMany([
    { _id: 1, code: 'WELCOME10', discount_type: 'rate',  discount_value: 10.00, min_order_amt:      0, max_usage: 1000, used_count: 0, expires_at: new Date('2025-12-31'), is_active: true, created_at: new Date() },
    { _id: 2, code: 'SAVE50000', discount_type: 'fixed', discount_value: 50000, min_order_amt: 300000, max_usage:  500, used_count: 0, expires_at: new Date('2025-06-30'), is_active: true, created_at: new Date() },
    { _id: 3, code: 'VIP20',     discount_type: 'rate',  discount_value: 20.00, min_order_amt: 500000, max_usage:  100, used_count: 0, expires_at: new Date('2025-12-31'), is_active: true, created_at: new Date() },
    { _id: 4, code: 'SUMMER15',  discount_type: 'rate',  discount_value: 15.00, min_order_amt: 100000, max_usage:  200, used_count: 0, expires_at: new Date('2025-08-31'), is_active: true, created_at: new Date() },
]);

// ── orders ─────────────────────────────────────────────────
db.orders.insertMany([
    { _id: 1,  customer_id: 1, status: 'delivered', total_amount: 1599000, shipping_address: '123 Teheran-ro, Gangnam-gu, Seoul',     tracking_number: 'TRACK-20240101-001', notes: null, created_at: new Date('2024-01-01'), updated_at: new Date('2024-01-01') },
    { _id: 2,  customer_id: 2, status: 'shipped',   total_amount: 2490000, shipping_address: '456 Dalmaji-gil, Haeundae-gu, Busan',   tracking_number: 'TRACK-20240102-001', notes: null, created_at: new Date('2024-01-02'), updated_at: new Date('2024-01-02') },
    { _id: 3,  customer_id: 3, status: 'confirmed', total_amount:   96000, shipping_address: '789 Dongdaegu-ro, Suseong-gu, Daegu',   tracking_number: null,                 notes: null, created_at: new Date('2024-01-03'), updated_at: new Date('2024-01-03') },
    { _id: 4,  customer_id: 4, status: 'pending',   total_amount: 1199000, shipping_address: '101 Songdo-daero, Yeonsu-gu, Incheon',  tracking_number: null,                 notes: null, created_at: new Date('2024-01-04'), updated_at: new Date('2024-01-04') },
    { _id: 5,  customer_id: 5, status: 'delivered', total_amount: 1690000, shipping_address: '202 Cheomdangwagi-ro, Buk-gu, Gwangju', tracking_number: 'TRACK-20240105-001', notes: null, created_at: new Date('2024-01-05'), updated_at: new Date('2024-01-05') },
    { _id: 6,  customer_id: 1, status: 'delivered', total_amount:   71000, shipping_address: '123 Teheran-ro, Gangnam-gu, Seoul',     tracking_number: 'TRACK-20240106-001', notes: null, created_at: new Date('2024-01-06'), updated_at: new Date('2024-01-06') },
    { _id: 7,  customer_id: 6, status: 'cancelled', total_amount:   39000, shipping_address: '303 Daehak-ro, Yuseong-gu, Daejeon',    tracking_number: null,                 notes: null, created_at: new Date('2024-01-07'), updated_at: new Date('2024-01-07') },
    { _id: 8,  customer_id: 7, status: 'refunded',  total_amount:   59000, shipping_address: '404 Hongik-ro, Mapo-gu, Seoul',         tracking_number: 'TRACK-20240108-001', notes: null, created_at: new Date('2024-01-08'), updated_at: new Date('2024-01-08') },
    { _id: 9,  customer_id: 8, status: 'confirmed', total_amount:  317000, shipping_address: '505 Olympic-ro, Songpa-gu, Seoul',      tracking_number: null,                 notes: null, created_at: new Date('2024-01-09'), updated_at: new Date('2024-01-09') },
    { _id: 10, customer_id: 2, status: 'delivered', total_amount:  139000, shipping_address: '456 Dalmaji-gil, Haeundae-gu, Busan',   tracking_number: 'TRACK-20240110-001', notes: null, created_at: new Date('2024-01-10'), updated_at: new Date('2024-01-10') },
]);

// ── order_items ────────────────────────────────────────────
db.order_items.insertMany([
    { _id: 1,  order_id: 1,  product_id: 1,  quantity: 1, unit_price: 1599000, discount_rate: 0.00 },
    { _id: 2,  order_id: 2,  product_id: 4,  quantity: 1, unit_price: 2490000, discount_rate: 0.00 },
    { _id: 3,  order_id: 3,  product_id: 10, quantity: 1, unit_price:   33000, discount_rate: 5.00 },
    { _id: 4,  order_id: 3,  product_id: 11, quantity: 1, unit_price:   38000, discount_rate: 5.00 },
    { _id: 5,  order_id: 3,  product_id: 12, quantity: 1, unit_price:   25000, discount_rate: 0.00 },
    { _id: 6,  order_id: 4,  product_id: 3,  quantity: 1, unit_price: 1199000, discount_rate: 0.00 },
    { _id: 7,  order_id: 5,  product_id: 5,  quantity: 1, unit_price: 1690000, discount_rate: 0.00 },
    { _id: 8,  order_id: 6,  product_id: 10, quantity: 1, unit_price:   33000, discount_rate: 5.00 },
    { _id: 9,  order_id: 6,  product_id: 11, quantity: 1, unit_price:   38000, discount_rate: 5.00 },
    { _id: 10, order_id: 7,  product_id: 8,  quantity: 1, unit_price:   39000, discount_rate: 0.00 },
    { _id: 11, order_id: 8,  product_id: 9,  quantity: 1, unit_price:   59000, discount_rate: 0.00 },
    { _id: 12, order_id: 9,  product_id: 6,  quantity: 1, unit_price:  139000, discount_rate: 0.00 },
    { _id: 13, order_id: 9,  product_id: 7,  quantity: 1, unit_price:  179000, discount_rate: 0.00 },
    { _id: 14, order_id: 9,  product_id: 12, quantity: 2, unit_price:   25000, discount_rate: 5.00 },
    { _id: 15, order_id: 10, product_id: 6,  quantity: 1, unit_price:  139000, discount_rate: 0.00 },
]);

// ── payments ───────────────────────────────────────────────
db.payments.insertMany([
    { _id: 1, order_id: 1,  amount: 1599000, method: 'card',            status: 'completed', transaction_id: 'TXN-2024-001', paid_at: new Date('2024-01-01T10:30:00'), created_at: new Date('2024-01-01') },
    { _id: 2, order_id: 2,  amount: 2490000, method: 'bank_transfer',   status: 'completed', transaction_id: 'TXN-2024-002', paid_at: new Date('2024-01-02T14:20:00'), created_at: new Date('2024-01-02') },
    { _id: 3, order_id: 3,  amount:   96000, method: 'card',            status: 'completed', transaction_id: 'TXN-2024-003', paid_at: new Date('2024-01-03T09:15:00'), created_at: new Date('2024-01-03') },
    { _id: 4, order_id: 5,  amount: 1690000, method: 'card',            status: 'completed', transaction_id: 'TXN-2024-005', paid_at: new Date('2024-01-05T16:45:00'), created_at: new Date('2024-01-05') },
    { _id: 5, order_id: 6,  amount:   71000, method: 'point',           status: 'completed', transaction_id: 'TXN-2024-006', paid_at: new Date('2024-01-06T11:30:00'), created_at: new Date('2024-01-06') },
    { _id: 6, order_id: 8,  amount:   59000, method: 'card',            status: 'refunded',  transaction_id: 'TXN-2024-008', paid_at: new Date('2024-01-08T13:00:00'), created_at: new Date('2024-01-08') },
    { _id: 7, order_id: 9,  amount:  317000, method: 'virtual_account', status: 'completed', transaction_id: 'TXN-2024-009', paid_at: new Date('2024-01-09T12:00:00'), created_at: new Date('2024-01-09') },
    { _id: 8, order_id: 10, amount:  139000, method: 'card',            status: 'completed', transaction_id: 'TXN-2024-010', paid_at: new Date('2024-01-10T10:00:00'), created_at: new Date('2024-01-10') },
]);

// ── reviews ────────────────────────────────────────────────
db.reviews.insertMany([
    { _id: 1, product_id: 1,  customer_id: 1, rating: 5, title: 'The best Galaxy yet',               content: 'Both the S Pen and the camera are excellent. Highly recommended!', is_verified: true,  created_at: new Date() },
    { _id: 2, product_id: 4,  customer_id: 2, rating: 5, title: 'Overwhelming M3 performance',       content: 'The M3 chip is astonishingly fast and the battery lasts all day.', is_verified: true,  created_at: new Date() },
    { _id: 3, product_id: 5,  customer_id: 5, rating: 4, title: 'Light and practical',               content: 'Very light for a 17-inch. Perfect for work!',                      is_verified: true,  created_at: new Date() },
    { _id: 4, product_id: 10, customer_id: 1, rating: 5, title: 'A must-read for developers',        content: 'It changed the way I write code.',                                 is_verified: true,  created_at: new Date() },
    { _id: 5, product_id: 11, customer_id: 6, rating: 4, title: 'Ideal for getting started with Go', content: 'Strongly recommended for anyone new to Go.',                       is_verified: false, created_at: new Date() },
    { _id: 6, product_id: 3,  customer_id: 3, rating: 3, title: 'Decent value for money',            content: 'Plenty of features, but the camera is a little disappointing.',    is_verified: true,  created_at: new Date() },
    { _id: 7, product_id: 6,  customer_id: 8, rating: 5, title: 'The ultimate mouse',                content: 'Ergonomic design that is easy on the wrist.',                      is_verified: true,  created_at: new Date() },
    { _id: 8, product_id: 9,  customer_id: 7, rating: 4, title: 'Pretty and comfortable',            content: 'Nice fabric and a flattering fit, so I wear it often.',            is_verified: true,  created_at: new Date() },
]);

// ── inventory_log ──────────────────────────────────────────
db.inventory_log.insertMany([
    { _id: 1,  product_id: 1,  change_type: 'purchase', quantity_change:  50, quantity_after:  50, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 2,  product_id: 2,  change_type: 'purchase', quantity_change:  30, quantity_after:  30, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 3,  product_id: 3,  change_type: 'purchase', quantity_change:  20, quantity_after:  20, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 4,  product_id: 4,  change_type: 'purchase', quantity_change:  15, quantity_after:  15, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 5,  product_id: 5,  change_type: 'purchase', quantity_change:  25, quantity_after:  25, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 6,  product_id: 6,  change_type: 'purchase', quantity_change: 100, quantity_after: 100, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 7,  product_id: 7,  change_type: 'purchase', quantity_change:  80, quantity_after:  80, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 8,  product_id: 8,  change_type: 'purchase', quantity_change: 200, quantity_after: 200, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 9,  product_id: 9,  change_type: 'purchase', quantity_change: 150, quantity_after: 150, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 10, product_id: 10, change_type: 'purchase', quantity_change: 100, quantity_after: 100, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 11, product_id: 11, change_type: 'purchase', quantity_change:  80, quantity_after:  80, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 12, product_id: 12, change_type: 'purchase', quantity_change: 300, quantity_after: 300, reference_id: null, notes: 'Initial stock-in', created_at: new Date() },
    { _id: 13, product_id: 1,  change_type: 'sale',     quantity_change:  -1, quantity_after:  49, reference_id: 1,    notes: 'Order #1',         created_at: new Date() },
    { _id: 14, product_id: 4,  change_type: 'sale',     quantity_change:  -1, quantity_after:  14, reference_id: 2,    notes: 'Order #2',         created_at: new Date() },
    { _id: 15, product_id: 10, change_type: 'sale',     quantity_change:  -1, quantity_after:  99, reference_id: 3,    notes: 'Order #3',         created_at: new Date() },
    { _id: 16, product_id: 11, change_type: 'sale',     quantity_change:  -1, quantity_after:  79, reference_id: 3,    notes: 'Order #3',         created_at: new Date() },
    { _id: 17, product_id: 12, change_type: 'sale',     quantity_change:  -1, quantity_after: 299, reference_id: 3,    notes: 'Order #3',         created_at: new Date() },
    { _id: 18, product_id: 3,  change_type: 'sale',     quantity_change:  -1, quantity_after:  19, reference_id: 4,    notes: 'Order #4',         created_at: new Date() },
    { _id: 19, product_id: 5,  change_type: 'sale',     quantity_change:  -1, quantity_after:  24, reference_id: 5,    notes: 'Order #5',         created_at: new Date() },
    { _id: 20, product_id: 10, change_type: 'sale',     quantity_change:  -1, quantity_after:  98, reference_id: 6,    notes: 'Order #6',         created_at: new Date() },
    { _id: 21, product_id: 11, change_type: 'sale',     quantity_change:  -1, quantity_after:  78, reference_id: 6,    notes: 'Order #6',         created_at: new Date() },
    { _id: 22, product_id: 8,  change_type: 'sale',     quantity_change:  -1, quantity_after: 199, reference_id: 7,    notes: 'Order #7',         created_at: new Date() },
    { _id: 23, product_id: 9,  change_type: 'sale',     quantity_change:  -1, quantity_after: 149, reference_id: 8,    notes: 'Order #8',         created_at: new Date() },
    { _id: 24, product_id: 6,  change_type: 'sale',     quantity_change:  -1, quantity_after:  99, reference_id: 9,    notes: 'Order #9',         created_at: new Date() },
    { _id: 25, product_id: 7,  change_type: 'sale',     quantity_change:  -1, quantity_after:  79, reference_id: 9,    notes: 'Order #9',         created_at: new Date() },
    { _id: 26, product_id: 12, change_type: 'sale',     quantity_change:  -2, quantity_after: 297, reference_id: 9,    notes: 'Order #9',         created_at: new Date() },
    { _id: 27, product_id: 6,  change_type: 'sale',     quantity_change:  -1, quantity_after:  98, reference_id: 10,   notes: 'Order #10',        created_at: new Date() },
]);

// ── wishlist ───────────────────────────────────────────────
db.wishlist.insertMany([
    { _id: 1,  customer_id: 1, product_id: 4,  added_at: new Date() },
    { _id: 2,  customer_id: 1, product_id: 7,  added_at: new Date() },
    { _id: 3,  customer_id: 2, product_id: 1,  added_at: new Date() },
    { _id: 4,  customer_id: 2, product_id: 6,  added_at: new Date() },
    { _id: 5,  customer_id: 3, product_id: 4,  added_at: new Date() },
    { _id: 6,  customer_id: 3, product_id: 5,  added_at: new Date() },
    { _id: 7,  customer_id: 4, product_id: 1,  added_at: new Date() },
    { _id: 8,  customer_id: 4, product_id: 2,  added_at: new Date() },
    { _id: 9,  customer_id: 5, product_id: 6,  added_at: new Date() },
    { _id: 10, customer_id: 5, product_id: 11, added_at: new Date() },
    { _id: 11, customer_id: 6, product_id: 4,  added_at: new Date() },
    { _id: 12, customer_id: 7, product_id: 5,  added_at: new Date() },
]);

// ── Create indexes ────────────────────────────────────────
db.categories.createIndex({ parent_id: 1 });
db.products.createIndex({ category_id: 1 });
db.products.createIndex({ sku: 1 }, { unique: true });
db.customers.createIndex({ email: 1 }, { unique: true });
db.orders.createIndex({ customer_id: 1 });
db.orders.createIndex({ status: 1, created_at: 1 });
db.order_items.createIndex({ order_id: 1 });
db.order_items.createIndex({ product_id: 1 });
db.payments.createIndex({ order_id: 1 });
db.reviews.createIndex({ product_id: 1 });
db.reviews.createIndex({ customer_id: 1 });
db.inventory_log.createIndex({ product_id: 1 });
db.inventory_log.createIndex({ created_at: 1 });
db.wishlist.createIndex({ customer_id: 1, product_id: 1 }, { unique: true });
db.coupons.createIndex({ code: 1 }, { unique: true });


// ── UTF-8 encoding verification documents ────────────────────
// Every other document in this file is ASCII. These are deliberately
// multibyte (Korean, Japanese, emoji) so a migration that loses the
// encoding shows up as mojibake instead of passing silently.
db.categories.insertMany([
    { _id: 11, name: '한글 카테고리',  description: '한글 인코딩 검증용 카테고리 - 가나다라마바사', parent_id: null, sort_order: 98, is_active: true, created_at: new Date() },
    { _id: 12, name: '日本語カテゴリ', description: '日本語エンコーディング検証 - あいうえお',      parent_id: null, sort_order: 99, is_active: true, created_at: new Date() },
]);

db.customers.insertMany([
    { _id: 9,  email: 'utf8.ko@example.com',    first_name: '지은',  last_name: '홍',    phone: '010-0000-0001', address: '서울 종로구 세종대로 1', city: '서울',   country: 'KR', grade: 'Bronze', is_active: true, created_at: new Date(), updated_at: new Date() },
    { _id: 10, email: 'utf8.emoji@example.com', first_name: 'Emoji', last_name: 'Test',  phone: '010-0000-0002', address: 'Migration ok ✅ 🚀 🐛',  city: 'Global', country: 'KR', grade: 'Bronze', is_active: true, created_at: new Date(), updated_at: new Date() },
]);

print('[MongoDB] shop_db setup complete.');
