-- ============================================================
-- hr_db — Human Resources Test Database
-- Includes: Tables, FK(ALTER), Views, Functions,
--           Procedures, Triggers, Events
-- Engine: MySQL 8.0+
-- ============================================================

-- The sample data contains 4-byte UTF-8 (emoji), which a utf8mb3 client
-- connection rejects with ERROR 1366. Declare the session charset here so
-- the file loads correctly however it is fed to the client.
SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE DATABASE IF NOT EXISTS hr_db
    CHARACTER SET utf8mb4
    COLLATE utf8mb4_unicode_ci;

USE hr_db;

-- ============================================================
-- TABLES
-- ============================================================

CREATE TABLE IF NOT EXISTS departments (
    dept_id     INT          NOT NULL AUTO_INCREMENT,
    name        VARCHAR(100) NOT NULL,
    code        VARCHAR(10)  NOT NULL,
    location    VARCHAR(100) DEFAULT NULL,
    budget      DECIMAL(15,2) DEFAULT 0.00,
    manager_id  INT          DEFAULT NULL,
    created_at  DATETIME     DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (dept_id),
    UNIQUE KEY uq_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS positions (
    position_id  INT          NOT NULL AUTO_INCREMENT,
    title        VARCHAR(100) NOT NULL,
    level        TINYINT      NOT NULL COMMENT '1=Junior 2=Mid 3=Senior 4=Lead 5=Director',
    min_salary   DECIMAL(12,2) DEFAULT 0.00,
    max_salary   DECIMAL(12,2) DEFAULT 0.00,
    PRIMARY KEY (position_id),
    CONSTRAINT chk_level CHECK (level BETWEEN 1 AND 5)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS employees (
    emp_id       INT          NOT NULL AUTO_INCREMENT,
    dept_id      INT          NOT NULL,
    position_id  INT          NOT NULL,
    emp_number   VARCHAR(20)  NOT NULL,
    first_name   VARCHAR(100) NOT NULL,
    last_name    VARCHAR(100) NOT NULL,
    email        VARCHAR(200) NOT NULL,
    phone        VARCHAR(20)  DEFAULT NULL,
    hire_date    DATE         NOT NULL,
    birth_date   DATE         DEFAULT NULL,
    gender       ENUM('M','F','O') DEFAULT NULL,
    current_salary DECIMAL(12,2) NOT NULL,
    status       ENUM('active','on_leave','resigned','terminated') DEFAULT 'active',
    manager_id   INT          DEFAULT NULL,
    created_at   DATETIME     DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME     DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (emp_id),
    UNIQUE KEY uq_emp_number (emp_number),
    UNIQUE KEY uq_email (email),
    KEY idx_dept (dept_id),
    KEY idx_position (position_id),
    KEY idx_manager (manager_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS salary_history (
    history_id   INT           NOT NULL AUTO_INCREMENT,
    emp_id       INT           NOT NULL,
    old_salary   DECIMAL(12,2) NOT NULL,
    new_salary   DECIMAL(12,2) NOT NULL,
    change_type  ENUM('hire','raise','adjustment','promotion','demotion') NOT NULL,
    change_reason TEXT,
    changed_by   INT           DEFAULT NULL,
    changed_at   DATETIME      DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (history_id),
    KEY idx_emp (emp_id),
    KEY idx_changed_at (changed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS leave_types (
    leave_type_id  INT          NOT NULL AUTO_INCREMENT,
    name           VARCHAR(100) NOT NULL,
    days_per_year  INT          DEFAULT 0,
    is_paid        TINYINT(1)   DEFAULT 1,
    PRIMARY KEY (leave_type_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS leave_requests (
    request_id   INT      NOT NULL AUTO_INCREMENT,
    emp_id       INT      NOT NULL,
    leave_type_id INT     NOT NULL,
    start_date   DATE     NOT NULL,
    end_date     DATE     NOT NULL,
    days_count   INT      NOT NULL,
    reason       TEXT,
    status       ENUM('pending','approved','rejected','cancelled') DEFAULT 'pending',
    approved_by  INT      DEFAULT NULL,
    requested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    processed_at DATETIME DEFAULT NULL,
    PRIMARY KEY (request_id),
    KEY idx_emp (emp_id),
    KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS performance_reviews (
    review_id       INT          NOT NULL AUTO_INCREMENT,
    emp_id          INT          NOT NULL,
    reviewer_id     INT          NOT NULL,
    review_period   VARCHAR(20)  NOT NULL COMMENT 'e.g. 2024-H1, 2023-Annual',
    score           TINYINT      NOT NULL COMMENT '1~5',
    rating          ENUM('Outstanding','Exceeds','Meets','Below','Unsatisfactory') NOT NULL,
    strengths       TEXT,
    improvements    TEXT,
    goals_next      TEXT,
    completed_at    DATETIME     DEFAULT NULL,
    created_at      DATETIME     DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (review_id),
    UNIQUE KEY uq_emp_period (emp_id, review_period),
    KEY idx_reviewer (reviewer_id),
    CONSTRAINT chk_pr_score CHECK (score BETWEEN 1 AND 5)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS projects (
    project_id   INT          NOT NULL AUTO_INCREMENT,
    name         VARCHAR(200) NOT NULL,
    code         VARCHAR(20)  NOT NULL,
    dept_id      INT          NOT NULL,
    leader_id    INT          DEFAULT NULL,
    status       ENUM('planning','active','on_hold','completed','cancelled') DEFAULT 'planning',
    start_date   DATE         DEFAULT NULL,
    end_date     DATE         DEFAULT NULL,
    budget       DECIMAL(15,2) DEFAULT 0.00,
    description  TEXT,
    created_at   DATETIME     DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id),
    UNIQUE KEY uq_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS project_members (
    member_id    INT      NOT NULL AUTO_INCREMENT,
    project_id   INT      NOT NULL,
    emp_id       INT      NOT NULL,
    role         VARCHAR(100) DEFAULT 'Member',
    joined_at    DATE     DEFAULT (CURRENT_DATE),
    left_at      DATE     DEFAULT NULL,
    PRIMARY KEY (member_id),
    UNIQUE KEY uq_project_emp (project_id, emp_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


-- ============================================================
-- FOREIGN KEYS (ALTER TABLE)
-- ============================================================

ALTER TABLE departments
    ADD CONSTRAINT fk_dept_manager
    FOREIGN KEY (manager_id) REFERENCES employees(emp_id)
    ON DELETE SET NULL ON UPDATE CASCADE;

ALTER TABLE employees
    ADD CONSTRAINT fk_emp_dept
    FOREIGN KEY (dept_id) REFERENCES departments(dept_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE employees
    ADD CONSTRAINT fk_emp_position
    FOREIGN KEY (position_id) REFERENCES positions(position_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE employees
    ADD CONSTRAINT fk_emp_manager
    FOREIGN KEY (manager_id) REFERENCES employees(emp_id)
    ON DELETE SET NULL ON UPDATE CASCADE;

ALTER TABLE salary_history
    ADD CONSTRAINT fk_sal_emp
    FOREIGN KEY (emp_id) REFERENCES employees(emp_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE salary_history
    ADD CONSTRAINT fk_sal_changed_by
    FOREIGN KEY (changed_by) REFERENCES employees(emp_id)
    ON DELETE SET NULL ON UPDATE CASCADE;

ALTER TABLE leave_requests
    ADD CONSTRAINT fk_leave_emp
    FOREIGN KEY (emp_id) REFERENCES employees(emp_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE leave_requests
    ADD CONSTRAINT fk_leave_type
    FOREIGN KEY (leave_type_id) REFERENCES leave_types(leave_type_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE leave_requests
    ADD CONSTRAINT fk_leave_approver
    FOREIGN KEY (approved_by) REFERENCES employees(emp_id)
    ON DELETE SET NULL ON UPDATE CASCADE;

ALTER TABLE performance_reviews
    ADD CONSTRAINT fk_pr_emp
    FOREIGN KEY (emp_id) REFERENCES employees(emp_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE performance_reviews
    ADD CONSTRAINT fk_pr_reviewer
    FOREIGN KEY (reviewer_id) REFERENCES employees(emp_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE projects
    ADD CONSTRAINT fk_proj_dept
    FOREIGN KEY (dept_id) REFERENCES departments(dept_id)
    ON DELETE RESTRICT ON UPDATE CASCADE;

ALTER TABLE projects
    ADD CONSTRAINT fk_proj_leader
    FOREIGN KEY (leader_id) REFERENCES employees(emp_id)
    ON DELETE SET NULL ON UPDATE CASCADE;

ALTER TABLE project_members
    ADD CONSTRAINT fk_pm_project
    FOREIGN KEY (project_id) REFERENCES projects(project_id)
    ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE project_members
    ADD CONSTRAINT fk_pm_emp
    FOREIGN KEY (emp_id) REFERENCES employees(emp_id)
    ON DELETE CASCADE ON UPDATE CASCADE;


-- ============================================================
-- VIEWS  (CREATE OR REPLACE VIEW is supported in MySQL)
-- ============================================================

CREATE OR REPLACE VIEW v_employee_directory AS
SELECT
    e.emp_id,
    e.emp_number,
    CONCAT(e.first_name, ' ', e.last_name)  AS full_name,
    e.email,
    e.phone,
    d.name                                  AS department,
    p.title                                 AS position,
    p.level                                 AS position_level,
    e.hire_date,
    TIMESTAMPDIFF(YEAR, e.hire_date, CURDATE()) AS years_of_service,
    e.status,
    CONCAT(m.first_name, ' ', m.last_name)  AS manager_name
FROM employees e
JOIN  departments d ON e.dept_id    = d.dept_id
JOIN  positions   p ON e.position_id = p.position_id
LEFT  JOIN employees m ON e.manager_id = m.emp_id
WHERE e.status = 'active';


CREATE OR REPLACE VIEW v_department_headcount AS
SELECT
    d.dept_id,
    d.name          AS department,
    d.code,
    d.location,
    d.budget,
    COUNT(e.emp_id) AS headcount,
    SUM(e.current_salary)                   AS total_salary_cost,
    ROUND(AVG(e.current_salary), 0)         AS avg_salary,
    CONCAT(m.first_name,' ',m.last_name)    AS manager_name
FROM departments d
LEFT JOIN employees e ON d.dept_id    = e.dept_id AND e.status = 'active'
LEFT JOIN employees m ON d.manager_id = m.emp_id
GROUP BY d.dept_id, d.name, d.code, d.location, d.budget, manager_name;


CREATE OR REPLACE VIEW v_pending_leaves AS
SELECT
    lr.request_id,
    lr.start_date,
    lr.end_date,
    lr.days_count,
    lr.reason,
    lr.requested_at,
    CONCAT(e.first_name,' ',e.last_name) AS employee_name,
    e.emp_number,
    d.name                               AS department,
    lt.name                              AS leave_type
FROM leave_requests lr
JOIN employees   e  ON lr.emp_id       = e.emp_id
JOIN departments d  ON e.dept_id       = d.dept_id
JOIN leave_types lt ON lr.leave_type_id = lt.leave_type_id
WHERE lr.status = 'pending'
ORDER BY lr.requested_at;


-- ============================================================
-- FUNCTIONS
-- ============================================================

DROP FUNCTION IF EXISTS fn_years_of_service;
DROP FUNCTION IF EXISTS fn_performance_rating_text;
DROP FUNCTION IF EXISTS fn_annual_salary;

DELIMITER $$

CREATE FUNCTION fn_years_of_service(p_hire_date DATE)
RETURNS INT
DETERMINISTIC
BEGIN
    RETURN TIMESTAMPDIFF(YEAR, p_hire_date, CURDATE());
END$$


CREATE FUNCTION fn_performance_rating_text(p_score TINYINT)
RETURNS VARCHAR(20)
DETERMINISTIC
BEGIN
    RETURN CASE p_score
        WHEN 5 THEN 'Outstanding'
        WHEN 4 THEN 'Exceeds'
        WHEN 3 THEN 'Meets'
        WHEN 2 THEN 'Below'
        WHEN 1 THEN 'Unsatisfactory'
        ELSE 'Unknown'
    END;
END$$


CREATE FUNCTION fn_annual_salary(p_monthly_salary DECIMAL(12,2))
RETURNS DECIMAL(14,2)
DETERMINISTIC
BEGIN
    RETURN ROUND(p_monthly_salary * 12, 2);
END$$

DELIMITER ;


-- ============================================================
-- PROCEDURES
-- ============================================================

DROP PROCEDURE IF EXISTS sp_hire_employee;
DROP PROCEDURE IF EXISTS sp_adjust_salary;
DROP PROCEDURE IF EXISTS sp_dept_salary_report;

DELIMITER $$

CREATE PROCEDURE sp_hire_employee(
    IN  p_dept_id      INT,
    IN  p_position_id  INT,
    IN  p_first_name   VARCHAR(100),
    IN  p_last_name    VARCHAR(100),
    IN  p_email        VARCHAR(200),
    IN  p_hire_date    DATE,
    IN  p_salary       DECIMAL(12,2),
    OUT p_emp_id       INT
)
BEGIN
    DECLARE v_emp_number VARCHAR(20);

    DECLARE EXIT HANDLER FOR SQLEXCEPTION
    BEGIN
        ROLLBACK;
        RESIGNAL;
    END;

    START TRANSACTION;

    SET v_emp_number = CONCAT('EMP', LPAD(FLOOR(RAND() * 900000 + 100000), 6, '0'));

    INSERT INTO employees
        (dept_id, position_id, emp_number, first_name, last_name,
         email, hire_date, current_salary)
    VALUES
        (p_dept_id, p_position_id, v_emp_number, p_first_name, p_last_name,
         p_email, p_hire_date, p_salary);

    SET p_emp_id = LAST_INSERT_ID();

    INSERT INTO salary_history
        (emp_id, old_salary, new_salary, change_type, change_reason)
    VALUES
        (p_emp_id, 0, p_salary, 'hire', 'Initial hire');

    COMMIT;
END$$


CREATE PROCEDURE sp_adjust_salary(
    IN p_emp_id       INT,
    IN p_new_salary   DECIMAL(12,2),
    IN p_change_type  VARCHAR(20),
    IN p_reason       TEXT,
    IN p_changed_by   INT
)
BEGIN
    DECLARE v_old_salary DECIMAL(12,2);

    SELECT current_salary INTO v_old_salary
    FROM   employees WHERE emp_id = p_emp_id;

    UPDATE employees
    SET    current_salary = p_new_salary, updated_at = NOW()
    WHERE  emp_id = p_emp_id;

    INSERT INTO salary_history
        (emp_id, old_salary, new_salary, change_type, change_reason, changed_by)
    VALUES
        (p_emp_id, v_old_salary, p_new_salary, p_change_type, p_reason, p_changed_by);
END$$


CREATE PROCEDURE sp_dept_salary_report(IN p_dept_id INT)
BEGIN
    SELECT
        CONCAT(e.first_name,' ',e.last_name)    AS employee,
        p.title                                  AS position,
        e.current_salary,
        fn_annual_salary(e.current_salary)       AS annual_salary,
        fn_years_of_service(e.hire_date)         AS years_of_service,
        (SELECT score FROM performance_reviews pr
         WHERE pr.emp_id = e.emp_id
         ORDER BY pr.created_at DESC LIMIT 1)    AS latest_score
    FROM employees e
    JOIN positions p ON e.position_id = p.position_id
    WHERE e.dept_id = p_dept_id AND e.status = 'active'
    ORDER BY e.current_salary DESC;
END$$

DELIMITER ;


-- ============================================================
-- TRIGGERS
-- ============================================================

DROP TRIGGER IF EXISTS trg_after_employee_salary_update;
DROP TRIGGER IF EXISTS trg_before_review_insert;
DROP TRIGGER IF EXISTS trg_after_leave_status_update;
DROP TRIGGER IF EXISTS trg_after_employee_resign;

DELIMITER $$

-- Record salary_history automatically when the salary changes
CREATE TRIGGER trg_after_employee_salary_update
AFTER UPDATE ON employees
FOR EACH ROW
BEGIN
    IF NEW.current_salary <> OLD.current_salary THEN
        INSERT INTO salary_history
            (emp_id, old_salary, new_salary, change_type, change_reason)
        VALUES
            (NEW.emp_id, OLD.current_salary, NEW.current_salary,
             'adjustment', 'Salary updated via direct record modification');
    END IF;
END$$


-- Validate the performance review score range
CREATE TRIGGER trg_before_review_insert
BEFORE INSERT ON performance_reviews
FOR EACH ROW
BEGIN
    IF NEW.score < 1 OR NEW.score > 5 THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'Performance score must be between 1 and 5';
    END IF;
    SET NEW.rating = fn_performance_rating_text(NEW.score);
END$$


-- Stamp processed_at automatically when leave is approved
CREATE TRIGGER trg_after_leave_status_update
AFTER UPDATE ON leave_requests
FOR EACH ROW
BEGIN
    IF NEW.status IN ('approved', 'rejected')
       AND OLD.status = 'pending'
    THEN
        UPDATE leave_requests
        SET    processed_at = NOW()
        WHERE  request_id = NEW.request_id;
    END IF;
END$$


-- End project memberships when an employee is terminated
CREATE TRIGGER trg_after_employee_resign
AFTER UPDATE ON employees
FOR EACH ROW
BEGIN
    IF NEW.status IN ('resigned','terminated')
       AND OLD.status NOT IN ('resigned','terminated')
    THEN
        UPDATE project_members
        SET    left_at = CURDATE()
        WHERE  emp_id = NEW.emp_id AND left_at IS NULL;
    END IF;
END$$

DELIMITER ;


-- ============================================================
-- EVENTS
-- ============================================================

SET GLOBAL event_scheduler = ON;

DROP EVENT IF EXISTS evt_archive_old_salary_history;
DROP EVENT IF EXISTS evt_auto_reject_stale_leaves;

DELIMITER $$

-- Daily at midnight: archive salary_history older than 3 months (logs only)
CREATE EVENT evt_archive_old_salary_history
ON SCHEDULE EVERY 1 DAY
STARTS (CURRENT_DATE + INTERVAL 1 DAY)
DO
BEGIN
    -- In production this would move the rows into an archive table
    DELETE FROM salary_history
    WHERE changed_at < DATE_SUB(NOW(), INTERVAL 3 YEAR)
      AND change_type = 'adjustment';
END$$


-- Every Monday at 09:00: auto-reject leave requests left pending too long
CREATE EVENT evt_auto_reject_stale_leaves
ON SCHEDULE EVERY 1 WEEK
STARTS (DATE_ADD(CURDATE(), INTERVAL (9 - WEEKDAY(CURDATE())) DAY)
        + INTERVAL 9 HOUR)
DO
BEGIN
    UPDATE leave_requests
    SET    status = 'rejected',
           processed_at = NOW()
    WHERE  status = 'pending'
      AND  requested_at < DATE_SUB(NOW(), INTERVAL 14 DAY);
END$$

DELIMITER ;


-- ============================================================
-- SAMPLE DATA
-- ============================================================

-- positions
INSERT INTO positions (title, level, min_salary, max_salary) VALUES
('Junior Engineer',    1, 3000000, 4000000),
('Engineer',           2, 4000000, 5500000),
('Senior Engineer',    3, 5500000, 7500000),
('Lead Engineer',      4, 7500000, 10000000),
('Engineering Director',5,10000000,15000000),
('Junior Analyst',     1, 2800000, 3800000),
('Analyst',            2, 3800000, 5000000),
('Senior Analyst',     3, 5000000, 7000000),
('Manager',            4, 7000000, 10000000),
('Director',           5, 10000000,15000000);

-- departments (manager_id is UPDATEd after the employees are inserted)
INSERT INTO departments (name, code, location, budget) VALUES
('Engineering',  'ENG', 'Gangnam-gu, Seoul', 2000000000),
('Product',      'PRD', 'Gangnam-gu, Seoul',  800000000),
('Data Science', 'DAT', 'Mapo-gu, Seoul',     600000000),
('Sales',        'SAL', 'Jung-gu, Seoul',     500000000),
('HR',           'HR',  'Gangnam-gu, Seoul',  300000000),
('Finance',      'FIN', 'Gangnam-gu, Seoul',  400000000);

-- employees (manager_id starts as NULL to avoid a circular reference)
INSERT INTO employees
    (dept_id, position_id, emp_number, first_name, last_name,
     email, hire_date, birth_date, gender, current_salary, status, manager_id)
VALUES
-- Engineering
(1, 5, 'EMP000001', 'Junho',  'Kim',  'junho.kim@company.com',   '2018-03-01', '1980-05-15', 'M', 12000000, 'active', NULL),
(1, 4, 'EMP000002', 'Sujin',  'Lee',  'sujin.lee@company.com',   '2019-06-01', '1985-09-20', 'F',  9000000, 'active', 1),
(1, 3, 'EMP000003', 'Minjun', 'Park', 'minjun.park@company.com', '2020-01-15', '1990-03-12', 'M',  6500000, 'active', 2),
(1, 3, 'EMP000004', 'Jiyeon', 'Choi', 'jiyeon.choi@company.com', '2020-07-01', '1991-11-25', 'F',  6200000, 'active', 2),
(1, 2, 'EMP000005', 'Dohyun', 'Jung', 'dohyun.jung@company.com', '2022-03-01', '1995-07-08', 'M',  4800000, 'active', 3),
(1, 1, 'EMP000006', 'Ayoung', 'Han',  'ayoung.han@company.com',  '2023-09-01', '1999-02-14', 'F',  3500000, 'active', 3),
-- Product
(2, 9, 'EMP000007', 'Sungmin', 'Oh',   'sungmin.oh@company.com',   '2019-01-15', '1983-04-30', 'M', 9500000, 'active', NULL),
(2, 8, 'EMP000008', 'Yura',    'Lim',  'yura.lim@company.com',     '2020-05-01', '1988-08-22', 'F', 6800000, 'active', 7),
(2, 7, 'EMP000009', 'Taeyang', 'Shin', 'taeyang.shin@company.com', '2021-11-01', '1993-01-05', 'M', 5200000, 'active', 7),
-- Data Science
(3, 4, 'EMP000010', 'Hyejin', 'Kang', 'hyejin.kang@company.com', '2020-02-01', '1987-06-18', 'F', 8500000, 'active', NULL),
(3, 3, 'EMP000011', 'Jaewon', 'Yoon', 'jaewon.yoon@company.com', '2021-04-01', '1992-12-03', 'M', 7000000, 'active', 10),
(3, 2, 'EMP000012', 'Sohee',  'Jang', 'sohee.jang@company.com',  '2022-08-01', '1996-09-27', 'F', 5000000, 'active', 10),
-- Sales
(4, 9, 'EMP000013', 'Hyunchul', 'Cho',   'hyunchul.cho@company.com',   '2018-07-01', '1979-03-11', 'M', 9000000, 'active', NULL),
(4, 8, 'EMP000014', 'Mina',     'Kwon',  'mina.kwon@company.com',      '2020-09-01', '1989-07-16', 'F', 6500000, 'active', 13),
(4, 6, 'EMP000015', 'Sanghyun', 'Hwang', 'sanghyun.hwang@company.com', '2023-02-01', '1997-11-09', 'M', 3200000, 'active', 13),
-- HR
(5, 9, 'EMP000016', 'Jihyun',  'Seo', 'jihyun.seo@company.com', '2019-04-01', '1984-01-28', 'F', 8000000, 'active', NULL),
(5, 7, 'EMP000017', 'Minseok', 'Ko',  'minseok.ko@company.com', '2021-07-01', '1994-05-22', 'M', 4500000, 'active', 16),
-- Finance
(6, 9, 'EMP000018', 'Eunjung', 'Moon', 'eunjung.moon@company.com', '2018-10-01', '1982-10-07', 'F', 8500000, 'active', NULL),
(6, 8, 'EMP000019', 'Sungho',  'Ryu',  'sungho.ryu@company.com',   '2020-12-01', '1990-04-19', 'M', 6000000, 'active', 18),
(6, 7, 'EMP000020', 'Yeseul',  'Nam',  'yeseul.nam@company.com',   '2022-05-01', '1995-08-31', 'F', 4800000, 'active', 18);

-- Set manager_id on departments
UPDATE departments SET manager_id =  1 WHERE dept_id = 1;
UPDATE departments SET manager_id =  7 WHERE dept_id = 2;
UPDATE departments SET manager_id = 10 WHERE dept_id = 3;
UPDATE departments SET manager_id = 13 WHERE dept_id = 4;
UPDATE departments SET manager_id = 16 WHERE dept_id = 5;
UPDATE departments SET manager_id = 18 WHERE dept_id = 6;

-- leave_types
INSERT INTO leave_types (name, days_per_year, is_paid) VALUES
('Annual Leave',       15, 1),
('Sick Leave',         60, 1),
('Family Event Leave',  5, 1),
('Unpaid Leave',       30, 0),
('Maternity Leave',    90, 1);

-- salary_history (hire events; the trigger only fires on UPDATE)
INSERT INTO salary_history (emp_id, old_salary, new_salary, change_type, change_reason) VALUES
(1, 0, 12000000, 'hire', 'Initial hire as Engineering Director'),
(2, 0,  9000000, 'hire', 'Initial hire as Lead Engineer'),
(3, 0,  6500000, 'hire', 'Initial hire as Senior Engineer'),
(4, 0,  6200000, 'hire', 'Initial hire as Senior Engineer'),
(5, 0,  4800000, 'hire', 'Initial hire as Engineer'),
(6, 0,  3500000, 'hire', 'Initial hire as Junior Engineer');

-- salary raises (this UPDATE fires trg_after_employee_salary_update)
UPDATE employees SET current_salary = 9500000, updated_at = NOW() WHERE emp_id = 2;
UPDATE employees SET current_salary = 7000000, updated_at = NOW() WHERE emp_id = 3;
UPDATE employees SET current_salary = 5200000, updated_at = NOW() WHERE emp_id = 5;

-- leave_requests
INSERT INTO leave_requests
    (emp_id, leave_type_id, start_date, end_date, days_count, reason, status, approved_by)
VALUES
( 3, 1, '2024-07-15', '2024-07-19', 5, 'Summer vacation',             'approved', 2),
( 5, 2, '2024-03-10', '2024-03-12', 3, 'Sick leave due to influenza', 'approved', 2),
( 6, 1, '2024-08-01', '2024-08-05', 5, 'Personal trip',               'approved', 2),
( 9, 1, '2024-06-24', '2024-06-28', 5, 'Trip to Jeju Island',         'approved', 7),
(12, 1, '2024-09-02', '2024-09-06', 5, 'Family trip',                 'pending',  NULL),
(15, 3, '2024-05-20', '2024-05-22', 3, 'Attending a wedding',         'approved', 13),
(17, 2, '2024-04-08', '2024-04-09', 2, 'Hospital appointment',        'approved', 16);

-- performance_reviews
INSERT INTO performance_reviews
    (emp_id, reviewer_id, review_period, score, rating, strengths, improvements, completed_at)
VALUES
( 3,  2, '2023-Annual', 4, 'Exceeds',     'Strong technical skills, outstanding team collaboration',           'Needs better documentation',                  '2024-01-15 15:00:00'),
( 4,  2, '2023-Annual', 3, 'Meets',       'Thorough code reviews',                                             'Needs to show more initiative',               '2024-01-16 15:00:00'),
( 5,  3, '2023-Annual', 4, 'Exceeds',     'Growing fast, excellent self-directed learning',                    'Needs to develop mentoring skills',           '2024-01-17 15:00:00'),
( 6,  3, '2023-Annual', 3, 'Meets',       'Diligent work attitude',                                            'Needs deeper technical expertise',            '2024-01-17 16:00:00'),
( 8,  7, '2023-Annual', 5, 'Outstanding', 'Outstanding UX sense, data-driven decision making',                 'None',                                        '2024-01-18 15:00:00'),
(11, 10, '2023-Annual', 4, 'Exceeds',     'Strong ML modeling capability',                                     'Needs a better grasp of the business domain', '2024-01-19 15:00:00'),
(14, 13, '2023-Annual', 5, 'Outstanding', 'Hit 150 percent of the sales target, excellent account management', 'Needs stronger internal collaboration',       '2024-01-20 15:00:00');

-- projects
INSERT INTO projects
    (name, code, dept_id, leader_id, status, start_date, end_date, budget, description)
VALUES
('CM-Centipede v3 Development', 'CM-CENTI-V3',   1,  2, 'active',    '2024-01-01', '2024-12-31', 500000000, 'Third-generation multi-cloud data migration platform'),
('ML-based Migration Analysis', 'ML-MIG-ANAL',   3, 10, 'active',    '2024-03-01', '2024-09-30', 200000000, 'AI model for analyzing migration patterns'),
('Enterprise Sales Expansion',  'ENT-SALES-24',  4, 13, 'active',    '2024-01-01', '2024-12-31', 100000000, 'Win new enterprise accounts and upsell existing customers'),
('HR System Enhancement',       'HR-SYS-UPG',    5, 16, 'planning',  '2024-07-01', '2024-12-31',  50000000, 'Add new features to the HR management system'),
('Legacy DB Migration',         'LEGACY-DB-MIG', 1,  3, 'completed', '2024-01-01', '2024-05-31',  80000000, 'Completed the on-premises Oracle to MariaDB migration');

-- project_members
INSERT INTO project_members (project_id, emp_id, role, joined_at) VALUES
(1,  2, 'Lead',     '2024-01-01'),
(1,  3, 'Backend',  '2024-01-01'),
(1,  4, 'Backend',  '2024-01-01'),
(1,  5, 'Backend',  '2024-02-01'),
(1,  8, 'Product',  '2024-01-15'),
(1, 11, 'ML Ops',   '2024-03-01'),
(2, 10, 'Lead',     '2024-03-01'),
(2, 11, 'ML Eng',   '2024-03-01'),
(2, 12, 'Data Eng', '2024-03-15'),
(3, 13, 'Lead',     '2024-01-01'),
(3, 14, 'Sales',    '2024-01-01'),
(3, 15, 'Sales',    '2024-02-01'),
(4, 16, 'Lead',     '2024-07-01'),
(4, 17, 'Analyst',  '2024-07-01'),
(5,  3, 'Lead',     '2024-01-01'),
(5,  4, 'Engineer', '2024-01-01');

-- ============================================================
-- UTF-8 ENCODING VERIFICATION ROWS
--
-- Every other row in this file is ASCII. These rows are deliberately
-- multibyte (Korean, Japanese, emoji) so a migration that loses the
-- charset or collation shows up as mojibake instead of passing silently.
-- ============================================================

INSERT INTO departments (name, code, location, budget) VALUES
('한글 부서명',   'UTF8K', '서울 종로구 세종대로 1', 100000000),
('日本語部門名', 'UTF8J', '東京都千代田区',         100000000);

INSERT INTO leave_types (name, days_per_year, is_paid) VALUES
('한글 휴가 유형 - 가나다', 3, 1),
('絵文字休暇 ✅🚀',         1, 0);
