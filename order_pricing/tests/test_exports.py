import json
import hashlib
from pathlib import Path
import shutil
import tempfile
import unittest

from jsonschema import ValidationError

from tests.support import FIXTURES, validate_package


class ExportContractTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.package = Path(self.temp.name) / "package"
        shutil.copytree(FIXTURES / "package", self.package)

    def edit_manifest(self, change):
        path = self.package / "manifest.json"
        value = json.loads(path.read_text())
        change(value)
        path.write_text(json.dumps(value))

    def test_valid_complete_package(self):
        manifest = validate_package(self.package)
        self.assertEqual(manifest["bounds"], {"minimum": 5, "maximum": 30})

    def test_additive_manifest_fields_are_compatible(self):
        self.edit_manifest(lambda m: m.update(optionalTrainingMetric=0.5))
        validate_package(self.package)

    def test_unsupported_major_and_required_capability_fail_before_loading(self):
        for key, value in (("adapterMajor", 2), ("contextMajor", 2),
                           ("requiredCapabilities", ["courier-presence.v2"])):
            with self.subTest(key=key):
                original = (self.package / "manifest.json").read_text()
                self.edit_manifest(lambda m: m.update({key: value}))
                with self.assertRaises((ValueError, ValidationError)):
                    validate_package(self.package)
                (self.package / "manifest.json").write_text(original)

    def test_corrupt_artifact_and_unlisted_file_are_rejected(self):
        artifact = self.package / "policy" / "example.py"
        artifact.write_text(artifact.read_text() + "\n# corrupt\n")
        with self.assertRaisesRegex(ValueError, "checksum"):
            validate_package(self.package)
        (self.package / "unlisted").write_text("not in manifest")
        with self.assertRaisesRegex(ValueError, "every payload"):
            validate_package(self.package)

    def test_missing_payload_is_rejected(self):
        (self.package / "requirements.lock").unlink()
        with self.assertRaises(ValueError):
            validate_package(self.package)

    def test_unsafe_path_is_rejected(self):
        self.edit_manifest(lambda m: m["files"].update({"../escape": "0" * 64}))
        with self.assertRaisesRegex(ValueError, "unsafe artifact path"):
            validate_package(self.package)

    def test_symlink_is_rejected(self):
        (self.package / "linked").symlink_to(self.package / "requirements.lock")
        with self.assertRaisesRegex(ValueError, "symlinks"):
            validate_package(self.package)

    def test_invalid_bounds_are_rejected(self):
        self.edit_manifest(lambda m: m.update(bounds={"minimum": 30, "maximum": 5}))
        with self.assertRaisesRegex(ValueError, "bounds"):
            validate_package(self.package)

    def test_unpinned_runtime_dependency_is_rejected(self):
        lock = self.package / "requirements.lock"
        lock.write_text("some-model-library>=1.0\n")
        self.edit_manifest(lambda m: m["files"].update({
            "requirements.lock": hashlib.sha256(lock.read_bytes()).hexdigest()
        }))
        with self.assertRaisesRegex(ValueError, "exact pins"):
            validate_package(self.package)


if __name__ == "__main__":
    unittest.main()
