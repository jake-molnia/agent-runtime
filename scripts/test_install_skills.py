import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest


spec = importlib.util.spec_from_file_location(
    "install_skills", Path(__file__).with_name("install-skills.py")
)
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


def archive(entries):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as output:
        for name, content in entries.items():
            member = tarfile.TarInfo(name)
            member.size = len(content)
            output.addfile(member, io.BytesIO(content))
    return buffer.getvalue()


class SkillInstallTest(unittest.TestCase):
    def source(self, data):
        return {
            "name": "fixture", "repository": "owner/skills", "revision": "a" * 40,
            "sha256": hashlib.sha256(data).hexdigest(), "include": ["skills", "LICENSE"],
            "skill_root": "skills", "aliases": {"tdd": "fixture-tdd"}, "replacements": [],
        }

    def test_catalog_retains_references_and_license_without_unselected_files(self):
        data = archive({
            "repo/skills/tdd/SKILL.md": b"---\nname: tdd\n---\nRead references/example.md",
            "repo/skills/tdd/references/example.md": b"supporting evidence",
            "repo/LICENSE": b"license terms",
            "repo/unselected": b"not part of bundle",
        })
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "fixture.tar.gz").write_bytes(data)
            bundles, catalog = root / "bundles", root / "catalog"
            installer.install({"version": 1, "sources": [self.source(data)]}, bundles, catalog, root)
            skill = catalog / "fixture-tdd"
            self.assertIn("name: fixture-tdd", (skill / "SKILL.md").read_text())
            self.assertEqual((skill / "references/example.md").read_text(), "supporting evidence")
            self.assertEqual((bundles / "fixture/LICENSE").read_text(), "license terms")
            self.assertFalse((bundles / "fixture/unselected").exists())
            files = json.loads((bundles / "files.json").read_text())
            self.assertEqual({item["path"] for item in files}, {
                "fixture/LICENSE", "fixture/skills/tdd/SKILL.md",
                "fixture/skills/tdd/references/example.md",
            })
            for item in files:
                self.assertEqual(item["sha256"], hashlib.sha256((bundles / item["path"]).read_bytes()).hexdigest())
            self.assertEqual((skill / "SKILL.md").stat().st_mode & 0o222, 0)
            # Restore directory write permission so temporary cleanup works as non-root.
            for path in bundles.rglob("*"):
                if path.is_dir():
                    path.chmod(0o755)
            bundles.chmod(0o755)
            catalog.chmod(0o755)

    def test_checksum_checked_before_extraction(self):
        data = archive({"repo/skills/demo/SKILL.md": b"trusted"})
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / "bundle"
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                installer.extract(data + b"changed", self.source(data), target)
            self.assertFalse(target.exists())

    def test_archive_cannot_escape_bundle(self):
        for name in ["repo/skills/../../outside", "/repo/skills/demo", "repo/skills\\outside"]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                data = archive({name: b"untrusted"})
                with self.assertRaisesRegex(ValueError, "unsafe archive path"):
                    installer.extract(data, self.source(data), Path(temporary) / "bundle")

    def test_archive_links_are_rejected(self):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as output:
            member = tarfile.TarInfo("repo/skills/demo/SKILL.md")
            member.type = tarfile.SYMTYPE
            member.linkname = "/etc/passwd"
            output.addfile(member)
        data = buffer.getvalue()
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(ValueError, "unsupported archive entry"):
                installer.extract(data, self.source(data), Path(temporary) / "bundle")


if __name__ == "__main__":
    unittest.main()
