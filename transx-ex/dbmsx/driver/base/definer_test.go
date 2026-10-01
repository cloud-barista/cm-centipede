package base

import (
	"bytes"
	"strings"
	"testing"
)

func TestStripDefiner(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// The statement that failed against RDS MySQL 8.4.
			name: "view",
			in:   "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v_customer_stats` AS select 1",
			want: "CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v_customer_stats` AS select 1",
		},
		{
			name: "function",
			in:   "CREATE DEFINER=`centipede`@`%` FUNCTION `fn_apply_discount`(p DECIMAL) RETURNS DECIMAL",
			want: "CREATE FUNCTION `fn_apply_discount`(p DECIMAL) RETURNS DECIMAL",
		},
		{
			name: "trigger",
			in:   "CREATE DEFINER=`root`@`localhost` TRIGGER `trg_after_order_item_insert` AFTER INSERT ON `t`",
			want: "CREATE TRIGGER `trg_after_order_item_insert` AFTER INSERT ON `t`",
		},
		{
			name: "event",
			in:   "CREATE DEFINER=`root`@`localhost` EVENT `evt_cleanup` ON SCHEDULE EVERY 1 DAY DO BEGIN END",
			want: "CREATE EVENT `evt_cleanup` ON SCHEDULE EVERY 1 DAY DO BEGIN END",
		},
		{
			// mysqldump wraps the clause in a version-gated comment of its own.
			// Emptying it leaves a comment MySQL parses and ignores.
			name: "mysqldump versioned comment",
			in:   "/*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER `t` BEFORE INSERT ON `x`",
			want: "/*!50017*/ /*!50003 TRIGGER `t` BEFORE INSERT ON `x`",
		},
		{
			name: "single quoted identifiers",
			in:   "CREATE DEFINER='admin'@'10.0.0.%' PROCEDURE `p`()",
			want: "CREATE PROCEDURE `p`()",
		},
		{
			name: "unquoted identifiers",
			in:   "CREATE DEFINER=root@localhost VIEW `v` AS select 1",
			want: "CREATE VIEW `v` AS select 1",
		},
		{
			name: "lower case keyword",
			in:   "create definer=`root`@`localhost` view `v` as select 1",
			want: "create view `v` as select 1",
		},
		{
			// SHOW CREATE TABLE never carries one, and must come back untouched.
			name: "create table unchanged",
			in:   "CREATE TABLE `orders` (\n  `id` int NOT NULL\n) ENGINE=InnoDB",
			want: "CREATE TABLE `orders` (\n  `id` int NOT NULL\n) ENGINE=InnoDB",
		},
		{
			name: "no definer unchanged",
			in:   "CREATE VIEW `v` AS select 1",
			want: "CREATE VIEW `v` AS select 1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripDefiner(c.in); got != c.want {
				t.Errorf("StripDefiner()\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

func TestDefinerFilterWriter(t *testing.T) {
	in := strings.Join([]string{
		"SET NAMES utf8mb4;",
		"/*!50001 CREATE ALGORITHM=UNDEFINED */",
		"/*!50013 DEFINER=`root`@`localhost` SQL SECURITY DEFINER */",
		"/*!50001 VIEW `v` AS select 1 */;",
		// A value that happens to contain the clause must survive intact.
		"INSERT INTO `audit` VALUES ('DEFINER=`root`@`localhost` was here');",
		"CREATE DEFINER=`root`@`localhost` TRIGGER `t` BEFORE INSERT ON `x`",
	}, "\n") + "\n"

	want := strings.Join([]string{
		"SET NAMES utf8mb4;",
		"/*!50001 CREATE ALGORITHM=UNDEFINED */",
		"/*!50013 SQL SECURITY DEFINER */",
		"/*!50001 VIEW `v` AS select 1 */;",
		"INSERT INTO `audit` VALUES ('DEFINER=`root`@`localhost` was here');",
		"CREATE TRIGGER `t` BEFORE INSERT ON `x`",
	}, "\n") + "\n"

	// Written in awkward chunks: the SSH stream arrives in arbitrary pieces, and
	// a clause split across two Write calls must still be removed.
	for _, chunk := range []int{1, 7, 64, len(in)} {
		var out bytes.Buffer
		w := NewDefinerFilterWriter(&out)
		for i := 0; i < len(in); i += chunk {
			end := i + chunk
			if end > len(in) {
				end = len(in)
			}
			if _, err := w.Write([]byte(in[i:end])); err != nil {
				t.Fatalf("chunk %d: write: %v", chunk, err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("chunk %d: close: %v", chunk, err)
		}
		if out.String() != want {
			t.Errorf("chunk %d:\n got: %q\nwant: %q", chunk, out.String(), want)
		}
	}
}

// A stream whose last line has no newline must still reach the destination.
func TestDefinerFilterWriterFlushesTrailingLine(t *testing.T) {
	var out bytes.Buffer
	w := NewDefinerFilterWriter(&out)
	if _, err := w.Write([]byte("CREATE DEFINER=`root`@`localhost` VIEW `v` AS select 1")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("line without a newline should stay buffered, got %q", out.String())
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if want := "CREATE VIEW `v` AS select 1"; out.String() != want {
		t.Errorf("\n got: %q\nwant: %q", out.String(), want)
	}
}
