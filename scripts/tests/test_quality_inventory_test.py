import importlib.util
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "test-quality-inventory.py"
spec = importlib.util.spec_from_file_location("quality_inventory", SCRIPT)
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)


class InventoryTest(unittest.TestCase):
    def test_nested_module_and_build_constraint_are_recorded(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "go.mod").write_text("module example.com/root\n")
            nested = root / "test" / "simulation"
            nested.mkdir(parents=True)
            (nested / "go.mod").write_text("module example.com/sim\n")
            (nested / "mainnet_test.go").write_text(
                "//go:build mainnet && !mocknet\n\npackage sim\n"
            )
            result = inventory.inventory(root)
            self.assertEqual(result["modules"], [".", "test/simulation"])
            self.assertEqual(
                result["build_constraints"],
                {"test/simulation/mainnet_test.go": "mainnet && !mocknet"},
            )
            self.assertEqual(
                result["test_file_counts_by_directory"], {"test/simulation": 1}
            )

    def test_disk_test_files_skip_directories_the_go_tool_ignores(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in (
                "a/x_test.go",
                "a/testdata/y_test.go",
                "_tools/z_test.go",
                ".cache/w_test.go",
                "vendor/v_test.go",
            ):
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                (root / name).write_text("package a\n")
            self.assertEqual(inventory.disk_test_files(root), {"a/x_test.go"})

    def test_selection_errors_flag_unselected_and_stale_entries(self):
        disk = {"a/selected_test.go", "a/testnet_test.go", "b/known_test.go"}
        selected = {"a/selected_test.go": ["unit-tests [mocknet]"]}
        known = {
            "b/known_test.go": "documented gap",
            "a/selected_test.go": "now selected",
            "c/deleted_test.go": "file removed",
        }
        self.assertEqual(
            inventory.selection_errors(disk, selected, known),
            [
                "test file not compiled by any CI job: a/testnet_test.go",
                "stale KNOWN_UNSELECTED entry (now selected or deleted): a/selected_test.go",
                "stale KNOWN_UNSELECTED entry (now selected or deleted): c/deleted_test.go",
            ],
        )
        self.assertEqual(
            inventory.selection_errors(
                disk,
                {**selected, "a/testnet_test.go": ["x"]},
                {"b/known_test.go": "gap"},
            ),
            [],
        )

    def test_filter_errors_flag_gocheck_tests_no_filter_runs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "x").mkdir()
            (root / "x/migrate_test.go").write_text(
                "package x\n"
                "func (s *MigrationSuite) TestMigrate1to2(c *C) {}\n"
                "func (s MigrationsCovSuite) TestSplit(c *check.C) {}\n"
                "func (UnnamedSuite) TestSkipped(c *C) {}\n"
                "func (*UnnamedPointerSuite) TestSkipped(c *C) {}\n"
                "func (s *MigrationSuite) helper(c *C) {}\n"
            )
            (root / "x/both_test.go").write_text(
                "package x\nfunc (s *OtherSuite) TestOther(c *C) {}\n"
            )
            filters = {
                "x/migrate_test.go": ["Migrate", "Migrate"],
                "x/both_test.go": ["Migrate", None],
            }
            self.assertEqual(
                inventory.filter_errors(root, filters),
                [
                    "gocheck test excluded by every -check.f filter that compiles it: "
                    "x/migrate_test.go: MigrationsCovSuite.TestSplit (filters: Migrate, Migrate)",
                    "gocheck test excluded by every -check.f filter that compiles it: "
                    "x/migrate_test.go: UnnamedSuite.TestSkipped (filters: Migrate, Migrate)",
                    "gocheck test excluded by every -check.f filter that compiles it: "
                    "x/migrate_test.go: UnnamedPointerSuite.TestSkipped "
                    "(filters: Migrate, Migrate)",
                ],
            )
            filters["x/migrate_test.go"] = ["Migrate|MigrationsCov"]
            self.assertEqual(len(inventory.filter_errors(root, filters)), 2)
            filters["x/migrate_test.go"] = ["Migrate|MigrationsCov|Unnamed"]
            self.assertEqual(inventory.filter_errors(root, filters), [])

    def test_selection_filters_match_the_makefile(self):
        makefile = (SCRIPT.parents[1] / "Makefile").read_text()
        for _, tags, patterns, _, check_filter in inventory.SELECTIONS:
            if check_filter:
                prefix = f"--tags {tags} --run TestPackage --test-arg=-check.f "
                self.assertTrue(
                    f"{prefix}--test-arg={check_filter} " in makefile
                    or f"{prefix}--test-arg='{check_filter}' " in makefile,
                    f"Makefile has no -check.f {check_filter} run for {patterns}",
                )


if __name__ == "__main__":
    unittest.main()
