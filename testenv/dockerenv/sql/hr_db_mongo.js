// ============================================================
// hr_db — Human Resources Test Database (MongoDB 7.0)
// mongosh < hr_db_mongo.js
// ============================================================

db = db.getSiblingDB('hr_db');

// ── Drop existing collections ──────────────────────────────
['departments','positions','employees','salary_history',
 'leave_types','leave_requests','performance_reviews',
 'projects','project_members']
    .forEach(name => { try { db[name].drop(); } catch(e) {} });

// ── positions ──────────────────────────────────────────────
db.positions.insertMany([
    { _id: 1,  title: 'Junior Engineer',      level: 1, min_salary: 3000000,  max_salary: 4000000 },
    { _id: 2,  title: 'Engineer',             level: 2, min_salary: 4000000,  max_salary: 5500000 },
    { _id: 3,  title: 'Senior Engineer',      level: 3, min_salary: 5500000,  max_salary: 7500000 },
    { _id: 4,  title: 'Lead Engineer',        level: 4, min_salary: 7500000,  max_salary: 10000000 },
    { _id: 5,  title: 'Engineering Director', level: 5, min_salary: 10000000, max_salary: 15000000 },
    { _id: 6,  title: 'Junior Analyst',       level: 1, min_salary: 2800000,  max_salary: 3800000 },
    { _id: 7,  title: 'Analyst',              level: 2, min_salary: 3800000,  max_salary: 5000000 },
    { _id: 8,  title: 'Senior Analyst',       level: 3, min_salary: 5000000,  max_salary: 7000000 },
    { _id: 9,  title: 'Manager',              level: 4, min_salary: 7000000,  max_salary: 10000000 },
    { _id: 10, title: 'Director',             level: 5, min_salary: 10000000, max_salary: 15000000 },
]);

// ── departments ────────────────────────────────────────────
db.departments.insertMany([
    { _id: 1, name: 'Engineering',  code: 'ENG', location: 'Gangnam-gu, Seoul', budget: 2000000000, manager_id: 1,  created_at: new Date() },
    { _id: 2, name: 'Product',      code: 'PRD', location: 'Gangnam-gu, Seoul', budget:  800000000, manager_id: 7,  created_at: new Date() },
    { _id: 3, name: 'Data Science', code: 'DAT', location: 'Mapo-gu, Seoul',    budget:  600000000, manager_id: 10, created_at: new Date() },
    { _id: 4, name: 'Sales',        code: 'SAL', location: 'Jung-gu, Seoul',    budget:  500000000, manager_id: 13, created_at: new Date() },
    { _id: 5, name: 'HR',           code: 'HR',  location: 'Gangnam-gu, Seoul', budget:  300000000, manager_id: 16, created_at: new Date() },
    { _id: 6, name: 'Finance',      code: 'FIN', location: 'Gangnam-gu, Seoul', budget:  400000000, manager_id: 18, created_at: new Date() },
]);

// ── employees ──────────────────────────────────────────────
db.employees.insertMany([
    // Engineering
    { _id: 1, dept_id: 1, position_id: 5, emp_number: 'EMP000001', first_name: 'Junho',  last_name: 'Kim',  email: 'junho.kim@company.com',   phone: null, hire_date: new Date('2018-03-01'), birth_date: new Date('1980-05-15'), gender: 'M', current_salary: 12000000, status: 'active', manager_id: null, created_at: new Date(), updated_at: new Date() },
    { _id: 2, dept_id: 1, position_id: 4, emp_number: 'EMP000002', first_name: 'Sujin',  last_name: 'Lee',  email: 'sujin.lee@company.com',   phone: null, hire_date: new Date('2019-06-01'), birth_date: new Date('1985-09-20'), gender: 'F', current_salary:  9500000, status: 'active', manager_id: 1,    created_at: new Date(), updated_at: new Date() },
    { _id: 3, dept_id: 1, position_id: 3, emp_number: 'EMP000003', first_name: 'Minjun', last_name: 'Park', email: 'minjun.park@company.com', phone: null, hire_date: new Date('2020-01-15'), birth_date: new Date('1990-03-12'), gender: 'M', current_salary:  7000000, status: 'active', manager_id: 2,    created_at: new Date(), updated_at: new Date() },
    { _id: 4, dept_id: 1, position_id: 3, emp_number: 'EMP000004', first_name: 'Jiyeon', last_name: 'Choi', email: 'jiyeon.choi@company.com', phone: null, hire_date: new Date('2020-07-01'), birth_date: new Date('1991-11-25'), gender: 'F', current_salary:  6200000, status: 'active', manager_id: 2,    created_at: new Date(), updated_at: new Date() },
    { _id: 5, dept_id: 1, position_id: 2, emp_number: 'EMP000005', first_name: 'Dohyun', last_name: 'Jung', email: 'dohyun.jung@company.com', phone: null, hire_date: new Date('2022-03-01'), birth_date: new Date('1995-07-08'), gender: 'M', current_salary:  5200000, status: 'active', manager_id: 3,    created_at: new Date(), updated_at: new Date() },
    { _id: 6, dept_id: 1, position_id: 1, emp_number: 'EMP000006', first_name: 'Ayoung', last_name: 'Han',  email: 'ayoung.han@company.com',  phone: null, hire_date: new Date('2023-09-01'), birth_date: new Date('1999-02-14'), gender: 'F', current_salary:  3500000, status: 'active', manager_id: 3,    created_at: new Date(), updated_at: new Date() },
    // Product
    { _id: 7, dept_id: 2, position_id: 9, emp_number: 'EMP000007', first_name: 'Sungmin', last_name: 'Oh',   email: 'sungmin.oh@company.com',   phone: null, hire_date: new Date('2019-01-15'), birth_date: new Date('1983-04-30'), gender: 'M', current_salary:  9500000, status: 'active', manager_id: null, created_at: new Date(), updated_at: new Date() },
    { _id: 8, dept_id: 2, position_id: 8, emp_number: 'EMP000008', first_name: 'Yura',    last_name: 'Lim',  email: 'yura.lim@company.com',     phone: null, hire_date: new Date('2020-05-01'), birth_date: new Date('1988-08-22'), gender: 'F', current_salary:  6800000, status: 'active', manager_id: 7,    created_at: new Date(), updated_at: new Date() },
    { _id: 9, dept_id: 2, position_id: 7, emp_number: 'EMP000009', first_name: 'Taeyang', last_name: 'Shin', email: 'taeyang.shin@company.com', phone: null, hire_date: new Date('2021-11-01'), birth_date: new Date('1993-01-05'), gender: 'M', current_salary:  5200000, status: 'active', manager_id: 7,    created_at: new Date(), updated_at: new Date() },
    // Data Science
    { _id: 10, dept_id: 3, position_id: 4, emp_number: 'EMP000010', first_name: 'Hyejin', last_name: 'Kang', email: 'hyejin.kang@company.com', phone: null, hire_date: new Date('2020-02-01'), birth_date: new Date('1987-06-18'), gender: 'F', current_salary:  8500000, status: 'active', manager_id: null, created_at: new Date(), updated_at: new Date() },
    { _id: 11, dept_id: 3, position_id: 3, emp_number: 'EMP000011', first_name: 'Jaewon', last_name: 'Yoon', email: 'jaewon.yoon@company.com', phone: null, hire_date: new Date('2021-04-01'), birth_date: new Date('1992-12-03'), gender: 'M', current_salary:  7000000, status: 'active', manager_id: 10,   created_at: new Date(), updated_at: new Date() },
    { _id: 12, dept_id: 3, position_id: 2, emp_number: 'EMP000012', first_name: 'Sohee',  last_name: 'Jang', email: 'sohee.jang@company.com',  phone: null, hire_date: new Date('2022-08-01'), birth_date: new Date('1996-09-27'), gender: 'F', current_salary:  5000000, status: 'active', manager_id: 10,   created_at: new Date(), updated_at: new Date() },
    // Sales
    { _id: 13, dept_id: 4, position_id: 9, emp_number: 'EMP000013', first_name: 'Hyunchul', last_name: 'Cho',   email: 'hyunchul.cho@company.com',   phone: null, hire_date: new Date('2018-07-01'), birth_date: new Date('1979-03-11'), gender: 'M', current_salary:  9000000, status: 'active', manager_id: null, created_at: new Date(), updated_at: new Date() },
    { _id: 14, dept_id: 4, position_id: 8, emp_number: 'EMP000014', first_name: 'Mina',     last_name: 'Kwon',  email: 'mina.kwon@company.com',      phone: null, hire_date: new Date('2020-09-01'), birth_date: new Date('1989-07-16'), gender: 'F', current_salary:  6500000, status: 'active', manager_id: 13,   created_at: new Date(), updated_at: new Date() },
    { _id: 15, dept_id: 4, position_id: 6, emp_number: 'EMP000015', first_name: 'Sanghyun', last_name: 'Hwang', email: 'sanghyun.hwang@company.com', phone: null, hire_date: new Date('2023-02-01'), birth_date: new Date('1997-11-09'), gender: 'M', current_salary:  3200000, status: 'active', manager_id: 13,   created_at: new Date(), updated_at: new Date() },
    // HR
    { _id: 16, dept_id: 5, position_id: 9, emp_number: 'EMP000016', first_name: 'Jihyun',  last_name: 'Seo', email: 'jihyun.seo@company.com', phone: null, hire_date: new Date('2019-04-01'), birth_date: new Date('1984-01-28'), gender: 'F', current_salary:  8000000, status: 'active', manager_id: null, created_at: new Date(), updated_at: new Date() },
    { _id: 17, dept_id: 5, position_id: 7, emp_number: 'EMP000017', first_name: 'Minseok', last_name: 'Ko',  email: 'minseok.ko@company.com', phone: null, hire_date: new Date('2021-07-01'), birth_date: new Date('1994-05-22'), gender: 'M', current_salary:  4500000, status: 'active', manager_id: 16,   created_at: new Date(), updated_at: new Date() },
    // Finance
    { _id: 18, dept_id: 6, position_id: 9, emp_number: 'EMP000018', first_name: 'Eunjung', last_name: 'Moon', email: 'eunjung.moon@company.com', phone: null, hire_date: new Date('2018-10-01'), birth_date: new Date('1982-10-07'), gender: 'F', current_salary:  8500000, status: 'active', manager_id: null, created_at: new Date(), updated_at: new Date() },
    { _id: 19, dept_id: 6, position_id: 8, emp_number: 'EMP000019', first_name: 'Sungho',  last_name: 'Ryu',  email: 'sungho.ryu@company.com',   phone: null, hire_date: new Date('2020-12-01'), birth_date: new Date('1990-04-19'), gender: 'M', current_salary:  6000000, status: 'active', manager_id: 18,   created_at: new Date(), updated_at: new Date() },
    { _id: 20, dept_id: 6, position_id: 7, emp_number: 'EMP000020', first_name: 'Yeseul',  last_name: 'Nam',  email: 'yeseul.nam@company.com',   phone: null, hire_date: new Date('2022-05-01'), birth_date: new Date('1995-08-31'), gender: 'F', current_salary:  4800000, status: 'active', manager_id: 18,   created_at: new Date(), updated_at: new Date() },
]);

// ── leave_types ────────────────────────────────────────────
db.leave_types.insertMany([
    { _id: 1, name: 'Annual Leave',       days_per_year: 15, is_paid: true },
    { _id: 2, name: 'Sick Leave',         days_per_year: 60, is_paid: true },
    { _id: 3, name: 'Family Event Leave', days_per_year:  5, is_paid: true },
    { _id: 4, name: 'Unpaid Leave',       days_per_year: 30, is_paid: false },
    { _id: 5, name: 'Maternity Leave',    days_per_year: 90, is_paid: true },
]);

// ── salary_history ─────────────────────────────────────────
db.salary_history.insertMany([
    { _id: 1, emp_id: 1, old_salary: 0,       new_salary: 12000000, change_type: 'hire',       change_reason: 'Initial hire as Engineering Director',          changed_by: null, changed_at: new Date('2018-03-01') },
    { _id: 2, emp_id: 2, old_salary: 0,       new_salary:  9000000, change_type: 'hire',       change_reason: 'Initial hire as Lead Engineer',                 changed_by: null, changed_at: new Date('2019-06-01') },
    { _id: 3, emp_id: 3, old_salary: 0,       new_salary:  6500000, change_type: 'hire',       change_reason: 'Initial hire as Senior Engineer',               changed_by: null, changed_at: new Date('2020-01-15') },
    { _id: 4, emp_id: 4, old_salary: 0,       new_salary:  6200000, change_type: 'hire',       change_reason: 'Initial hire as Senior Engineer',               changed_by: null, changed_at: new Date('2020-07-01') },
    { _id: 5, emp_id: 5, old_salary: 0,       new_salary:  4800000, change_type: 'hire',       change_reason: 'Initial hire as Engineer',                      changed_by: null, changed_at: new Date('2022-03-01') },
    { _id: 6, emp_id: 6, old_salary: 0,       new_salary:  3500000, change_type: 'hire',       change_reason: 'Initial hire as Junior Engineer',               changed_by: null, changed_at: new Date('2023-09-01') },
    { _id: 7, emp_id: 2, old_salary: 9000000, new_salary:  9500000, change_type: 'adjustment', change_reason: 'Salary updated via direct record modification', changed_by: null, changed_at: new Date() },
    { _id: 8, emp_id: 3, old_salary: 6500000, new_salary:  7000000, change_type: 'adjustment', change_reason: 'Salary updated via direct record modification', changed_by: null, changed_at: new Date() },
    { _id: 9, emp_id: 5, old_salary: 4800000, new_salary:  5200000, change_type: 'adjustment', change_reason: 'Salary updated via direct record modification', changed_by: null, changed_at: new Date() },
]);

// ── leave_requests ─────────────────────────────────────────
db.leave_requests.insertMany([
    { _id: 1, emp_id: 3,  leave_type_id: 1, start_date: new Date('2024-07-15'), end_date: new Date('2024-07-19'), days_count: 5, reason: 'Summer vacation',             status: 'approved', approved_by: 2,    requested_at: new Date('2024-07-01'), processed_at: new Date('2024-07-02') },
    { _id: 2, emp_id: 5,  leave_type_id: 2, start_date: new Date('2024-03-10'), end_date: new Date('2024-03-12'), days_count: 3, reason: 'Sick leave due to influenza', status: 'approved', approved_by: 2,    requested_at: new Date('2024-03-09'), processed_at: new Date('2024-03-09') },
    { _id: 3, emp_id: 6,  leave_type_id: 1, start_date: new Date('2024-08-01'), end_date: new Date('2024-08-05'), days_count: 5, reason: 'Personal trip',               status: 'approved', approved_by: 2,    requested_at: new Date('2024-07-20'), processed_at: new Date('2024-07-21') },
    { _id: 4, emp_id: 9,  leave_type_id: 1, start_date: new Date('2024-06-24'), end_date: new Date('2024-06-28'), days_count: 5, reason: 'Trip to Jeju Island',         status: 'approved', approved_by: 7,    requested_at: new Date('2024-06-10'), processed_at: new Date('2024-06-11') },
    { _id: 5, emp_id: 12, leave_type_id: 1, start_date: new Date('2024-09-02'), end_date: new Date('2024-09-06'), days_count: 5, reason: 'Family trip',                 status: 'pending',  approved_by: null, requested_at: new Date('2024-08-20'), processed_at: null },
    { _id: 6, emp_id: 15, leave_type_id: 3, start_date: new Date('2024-05-20'), end_date: new Date('2024-05-22'), days_count: 3, reason: 'Attending a wedding',         status: 'approved', approved_by: 13,   requested_at: new Date('2024-05-10'), processed_at: new Date('2024-05-11') },
    { _id: 7, emp_id: 17, leave_type_id: 2, start_date: new Date('2024-04-08'), end_date: new Date('2024-04-09'), days_count: 2, reason: 'Hospital appointment',        status: 'approved', approved_by: 16,   requested_at: new Date('2024-04-07'), processed_at: new Date('2024-04-07') },
]);

// ── performance_reviews ────────────────────────────────────
db.performance_reviews.insertMany([
    { _id: 1, emp_id: 3,  reviewer_id: 2,  review_period: '2023-Annual', score: 4, rating: 'Exceeds',     strengths: 'Strong technical skills, outstanding team collaboration',           improvements: 'Needs better documentation',                  goals_next: null, completed_at: new Date('2024-01-15T15:00:00'), created_at: new Date('2024-01-15') },
    { _id: 2, emp_id: 4,  reviewer_id: 2,  review_period: '2023-Annual', score: 3, rating: 'Meets',       strengths: 'Thorough code reviews',                                             improvements: 'Needs to show more initiative',               goals_next: null, completed_at: new Date('2024-01-16T15:00:00'), created_at: new Date('2024-01-16') },
    { _id: 3, emp_id: 5,  reviewer_id: 3,  review_period: '2023-Annual', score: 4, rating: 'Exceeds',     strengths: 'Growing fast, excellent self-directed learning',                    improvements: 'Needs to develop mentoring skills',           goals_next: null, completed_at: new Date('2024-01-17T15:00:00'), created_at: new Date('2024-01-17') },
    { _id: 4, emp_id: 6,  reviewer_id: 3,  review_period: '2023-Annual', score: 3, rating: 'Meets',       strengths: 'Diligent work attitude',                                            improvements: 'Needs deeper technical expertise',            goals_next: null, completed_at: new Date('2024-01-17T16:00:00'), created_at: new Date('2024-01-17') },
    { _id: 5, emp_id: 8,  reviewer_id: 7,  review_period: '2023-Annual', score: 5, rating: 'Outstanding', strengths: 'Outstanding UX sense, data-driven decision making',                 improvements: 'None',                                        goals_next: null, completed_at: new Date('2024-01-18T15:00:00'), created_at: new Date('2024-01-18') },
    { _id: 6, emp_id: 11, reviewer_id: 10, review_period: '2023-Annual', score: 4, rating: 'Exceeds',     strengths: 'Strong ML modeling capability',                                     improvements: 'Needs a better grasp of the business domain', goals_next: null, completed_at: new Date('2024-01-19T15:00:00'), created_at: new Date('2024-01-19') },
    { _id: 7, emp_id: 14, reviewer_id: 13, review_period: '2023-Annual', score: 5, rating: 'Outstanding', strengths: 'Hit 150 percent of the sales target, excellent account management', improvements: 'Needs stronger internal collaboration',       goals_next: null, completed_at: new Date('2024-01-20T15:00:00'), created_at: new Date('2024-01-20') },
]);

// ── projects ───────────────────────────────────────────────
db.projects.insertMany([
    { _id: 1, name: 'CM-Centipede v3 Development', code: 'CM-CENTI-V3',   dept_id: 1, leader_id: 2,  status: 'active',    start_date: new Date('2024-01-01'), end_date: new Date('2024-12-31'), budget: 500000000, description: 'Third-generation multi-cloud data migration platform',      created_at: new Date() },
    { _id: 2, name: 'ML-based Migration Analysis', code: 'ML-MIG-ANAL',   dept_id: 3, leader_id: 10, status: 'active',    start_date: new Date('2024-03-01'), end_date: new Date('2024-09-30'), budget: 200000000, description: 'AI model for analyzing migration patterns',                 created_at: new Date() },
    { _id: 3, name: 'Enterprise Sales Expansion',  code: 'ENT-SALES-24',  dept_id: 4, leader_id: 13, status: 'active',    start_date: new Date('2024-01-01'), end_date: new Date('2024-12-31'), budget: 100000000, description: 'Win new enterprise accounts and upsell existing customers', created_at: new Date() },
    { _id: 4, name: 'HR System Enhancement',       code: 'HR-SYS-UPG',    dept_id: 5, leader_id: 16, status: 'planning',  start_date: new Date('2024-07-01'), end_date: new Date('2024-12-31'), budget:  50000000, description: 'Add new features to the HR management system',              created_at: new Date() },
    { _id: 5, name: 'Legacy DB Migration',         code: 'LEGACY-DB-MIG', dept_id: 1, leader_id: 3,  status: 'completed', start_date: new Date('2024-01-01'), end_date: new Date('2024-05-31'), budget:  80000000, description: 'Completed the on-premises Oracle to MongoDB migration',     created_at: new Date() },
]);

// ── project_members ────────────────────────────────────────
db.project_members.insertMany([
    { _id: 1,  project_id: 1, emp_id: 2,  role: 'Lead',     joined_at: new Date('2024-01-01'), left_at: null },
    { _id: 2,  project_id: 1, emp_id: 3,  role: 'Backend',  joined_at: new Date('2024-01-01'), left_at: null },
    { _id: 3,  project_id: 1, emp_id: 4,  role: 'Backend',  joined_at: new Date('2024-01-01'), left_at: null },
    { _id: 4,  project_id: 1, emp_id: 5,  role: 'Backend',  joined_at: new Date('2024-02-01'), left_at: null },
    { _id: 5,  project_id: 1, emp_id: 8,  role: 'Product',  joined_at: new Date('2024-01-15'), left_at: null },
    { _id: 6,  project_id: 1, emp_id: 11, role: 'ML Ops',   joined_at: new Date('2024-03-01'), left_at: null },
    { _id: 7,  project_id: 2, emp_id: 10, role: 'Lead',     joined_at: new Date('2024-03-01'), left_at: null },
    { _id: 8,  project_id: 2, emp_id: 11, role: 'ML Eng',   joined_at: new Date('2024-03-01'), left_at: null },
    { _id: 9,  project_id: 2, emp_id: 12, role: 'Data Eng', joined_at: new Date('2024-03-15'), left_at: null },
    { _id: 10, project_id: 3, emp_id: 13, role: 'Lead',     joined_at: new Date('2024-01-01'), left_at: null },
    { _id: 11, project_id: 3, emp_id: 14, role: 'Sales',    joined_at: new Date('2024-01-01'), left_at: null },
    { _id: 12, project_id: 3, emp_id: 15, role: 'Sales',    joined_at: new Date('2024-02-01'), left_at: null },
    { _id: 13, project_id: 4, emp_id: 16, role: 'Lead',     joined_at: new Date('2024-07-01'), left_at: null },
    { _id: 14, project_id: 4, emp_id: 17, role: 'Analyst',  joined_at: new Date('2024-07-01'), left_at: null },
    { _id: 15, project_id: 5, emp_id: 3,  role: 'Lead',     joined_at: new Date('2024-01-01'), left_at: new Date('2024-05-31') },
    { _id: 16, project_id: 5, emp_id: 4,  role: 'Engineer', joined_at: new Date('2024-01-01'), left_at: new Date('2024-05-31') },
]);

// ── Create indexes ────────────────────────────────────────
db.departments.createIndex({ code: 1 }, { unique: true });
db.departments.createIndex({ manager_id: 1 });
db.positions.createIndex({ level: 1 });
db.employees.createIndex({ emp_number: 1 }, { unique: true });
db.employees.createIndex({ email: 1 },      { unique: true });
db.employees.createIndex({ dept_id: 1 });
db.employees.createIndex({ position_id: 1 });
db.employees.createIndex({ manager_id: 1 });
db.salary_history.createIndex({ emp_id: 1 });
db.salary_history.createIndex({ changed_at: 1 });
db.leave_requests.createIndex({ emp_id: 1 });
db.leave_requests.createIndex({ status: 1 });
db.performance_reviews.createIndex({ emp_id: 1, review_period: 1 }, { unique: true });
db.performance_reviews.createIndex({ reviewer_id: 1 });
db.projects.createIndex({ code: 1 }, { unique: true });
db.projects.createIndex({ dept_id: 1 });
db.project_members.createIndex({ project_id: 1, emp_id: 1 }, { unique: true });


// ── UTF-8 encoding verification documents ────────────────────
// Every other document in this file is ASCII. These are deliberately
// multibyte (Korean, Japanese, emoji) so a migration that loses the
// encoding shows up as mojibake instead of passing silently.
db.departments.insertMany([
    { _id: 7, name: '한글 부서명',   code: 'UTF8K', location: '서울 종로구 세종대로 1', budget: 100000000, manager_id: null, created_at: new Date() },
    { _id: 8, name: '日本語部門名', code: 'UTF8J', location: '東京都千代田区',         budget: 100000000, manager_id: null, created_at: new Date() },
]);

db.leave_types.insertMany([
    { _id: 6, name: '한글 휴가 유형 - 가나다', days_per_year: 3, is_paid: true },
    { _id: 7, name: '絵文字休暇 ✅🚀',         days_per_year: 1, is_paid: false },
]);

print('[MongoDB] hr_db setup complete.');
