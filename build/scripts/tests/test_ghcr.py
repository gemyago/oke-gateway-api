import sys
import os
from typing import List
import unittest
import random
import re
from unittest.mock import MagicMock, patch
from faker import Faker
from datetime import datetime, timedelta, timezone

import requests

fake = Faker()

# Add the parent directory to sys.path to import the ghcr module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '..')))
from ghcr import (
    GitHubTokenProvider,
    list_versions, 
    find_versions_to_clean,
    remove_version,
    AuthenticationError, 
    PackageVersion,
    CleanupAction
)


def create_random_package_version(**overrides) -> PackageVersion:
    """
    Create a random PackageVersion object for testing purposes.
    
    Args:
        overrides: Optional keyword arguments to override default values

    Returns:
        A randomly generated PackageVersion object
    """
    
    version = {
        "id": fake.random_int(min=1000, max=9999),
        "name": fake.name(),
        "url": fake.uri(),
        "package_html_url": fake.uri(),
        "created_at": fake.past_datetime().isoformat().replace('+00:00', 'Z'),
        "updated_at": fake.past_datetime().isoformat().replace('+00:00', 'Z'),
        "html_url": fake.uri(),
        "metadata": {
            "container": {
                "tags": fake.words()
            }
        }
    }
    version.update(overrides)
    return PackageVersion(**version)

class TestGitHubTokenProvider(unittest.TestCase):
    def test_get_token_from_env(self):
        """Test getting token from environment variable"""
        mock_environ = {'GITHUB_TOKEN': 'test-token-from-env'}
        
        # Mock subprocess to ensure it's not used when env var is available
        mock_subprocess = MagicMock()
        
        provider = GitHubTokenProvider(environ=mock_environ, subprocess_module=mock_subprocess)
        token = provider.get_token()
        
        self.assertEqual(token, 'test-token-from-env')
        # Verify subprocess was not called
        mock_subprocess.run.assert_not_called()
        
        # Verify the token is memoized (second call doesn't try to lookup again)
        mock_environ.clear()
        token2 = provider.get_token()
        self.assertEqual(token2, 'test-token-from-env')
    
    def test_get_token_from_gh_cli(self):
        """Test getting token from GitHub CLI when env var is not available"""
        # Empty environment - no GITHUB_TOKEN
        mock_environ = {}
        
        # Mock successful subprocess response
        mock_process = MagicMock()
        mock_process.returncode = 0
        mock_process.stdout = "test-token-from-gh-cli\n"
        
        mock_subprocess = MagicMock()
        mock_subprocess.run.return_value = mock_process
        
        provider = GitHubTokenProvider(environ=mock_environ, subprocess_module=mock_subprocess)
        token = provider.get_token()
        
        self.assertEqual(token, 'test-token-from-gh-cli')
        # Verify subprocess was called with correct arguments
        mock_subprocess.run.assert_called_once_with(
            ["gh", "auth", "token"],
            capture_output=True,
            text=True,
            check=False
        )
        
        # Verify the token is memoized (second call doesn't call subprocess again)
        mock_subprocess.reset_mock()
        token2 = provider.get_token()
        self.assertEqual(token2, 'test-token-from-gh-cli')
        mock_subprocess.run.assert_not_called()
    
    def test_clear_token(self):
        """Test clearing the cached token"""
        mock_environ = {'GITHUB_TOKEN': 'test-token-from-env'}
        provider = GitHubTokenProvider(environ=mock_environ)
        
        # First call should retrieve the token
        token1 = provider.get_token()
        self.assertEqual(token1, 'test-token-from-env')
        
        # Clear the environment and the token cache
        mock_environ.clear()
        provider.clear_token()
        
        # Mock subprocess for second attempt
        mock_process = MagicMock()
        mock_process.returncode = 0
        mock_process.stdout = "test-token-from-gh-cli\n"
        
        mock_subprocess = MagicMock()
        mock_subprocess.run.return_value = mock_process
        provider._subprocess = mock_subprocess
        
        # Second call should try to get the token from gh cli
        token2 = provider.get_token()
        self.assertEqual(token2, 'test-token-from-gh-cli')
        mock_subprocess.run.assert_called_once()
        
    def test_gh_cli_not_found(self):
        """Test handling when GitHub CLI is not installed"""
        # Empty environment - no GITHUB_TOKEN
        mock_environ = {}
        
        # Mock subprocess that raises FileNotFoundError (gh not installed)
        mock_subprocess = MagicMock()
        mock_subprocess.run.side_effect = FileNotFoundError("No such file or directory: 'gh'")
        
        provider = GitHubTokenProvider(environ=mock_environ, subprocess_module=mock_subprocess)
        
        # Should raise AuthenticationError
        with self.assertRaises(AuthenticationError) as context:
            provider.get_token()
        
        # Verify error message contains helpful instructions
        error_message = str(context.exception)
        self.assertIn("Unable to retrieve GitHub token", error_message)
        self.assertIn("Set the GITHUB_TOKEN environment variable", error_message)
        self.assertIn("Install and authenticate with GitHub CLI", error_message)
    
    def test_gh_cli_error(self):
        """Test handling when GitHub CLI returns an error"""
        # Empty environment - no GITHUB_TOKEN
        mock_environ = {}
        
        # Mock failed subprocess response
        mock_process = MagicMock()
        mock_process.returncode = 1
        mock_process.stdout = ""
        
        mock_subprocess = MagicMock()
        mock_subprocess.run.return_value = mock_process
        
        provider = GitHubTokenProvider(environ=mock_environ, subprocess_module=mock_subprocess)
        
        # Should raise AuthenticationError
        with self.assertRaises(AuthenticationError) as context:
            provider.get_token()
        
        # Verify error message contains helpful instructions
        error_message = str(context.exception)
        self.assertIn("Unable to retrieve GitHub token", error_message)


class TestListVersions(unittest.TestCase):
    def test_list_versions_success(self):
        """Test listing versions with successful API response"""
        # Mock token provider
        mock_token_provider = MagicMock()
        mock_token_provider.get_token.return_value = "test-token"
        
        # Generate random package versions
        num_versions = random.randint(3, 5)
        mock_versions = [create_random_package_version() for _ in range(num_versions)]
        
        # Sample API response
        mock_response = MagicMock()
        mock_response.json.return_value = mock_versions
        mock_response.headers = {}  # No Link header means only one page
        
        # Mock requests module
        mock_requests = MagicMock()
        mock_requests.get.return_value = mock_response
        
        # Call the function
        result = list_versions(
            namespace="user/test",
            package_name="test-package",
            token_provider=mock_token_provider,
            requests_module=mock_requests
        )
        
        # Verify API was called correctly
        mock_requests.get.assert_called_once_with(
            "https://api.github.com/user/test/packages/container/test-package/versions?per_page=100",
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": "Bearer test-token",
                "X-GitHub-Api-Version": "2022-11-28"
            }
        )
        
        # Verify the response was parsed correctly
        self.assertEqual(len(result), num_versions)
        # Verify each result matches the original mock data
        for i, version in enumerate(result):
            self.assertEqual(version["id"], mock_versions[i]["id"])
            self.assertEqual(version["name"], mock_versions[i]["name"])
            self.assertEqual(version["metadata"]["container"]["tags"], 
                             mock_versions[i]["metadata"]["container"]["tags"])

    def test_list_versions_pagination(self):
        """Test listing versions with pagination"""
        # Mock token provider
        mock_token_provider = MagicMock()
        mock_token_provider.get_token.return_value = "test-token"
        
        # Create mock versions for two pages
        first_page_versions = [create_random_package_version() for _ in range(3)]
        second_page_versions = [create_random_package_version() for _ in range(2)]
        
        # Create mock responses for pagination
        first_response = MagicMock()
        first_response.json.return_value = first_page_versions
        first_response.headers = {
            'Link': '<https://api.github.com/user/test/packages/container/test-package/versions?page=2&per_page=100>; rel="next", '
                   '<https://api.github.com/user/test/packages/container/test-package/versions?page=199&per_page=100>; rel="last"'
        }
        
        second_response = MagicMock()
        second_response.json.return_value = second_page_versions
        second_response.headers = {} # No Link header means this is the last page
        
        # Mock requests to return different responses for different URLs
        mock_requests = MagicMock()
        mock_requests.get.side_effect = [first_response, second_response]
        
        # Call the function
        result = list_versions(
            namespace="user/test",
            package_name="test-package",
            token_provider=mock_token_provider,
            requests_module=mock_requests
        )
        
        # Verify API was called for both pages
        expected_calls = [
            unittest.mock.call(
                "https://api.github.com/user/test/packages/container/test-package/versions?per_page=100",
                headers={
                    "Accept": "application/vnd.github+json",
                    "Authorization": "Bearer test-token",
                    "X-GitHub-Api-Version": "2022-11-28"
                }
            ),
            unittest.mock.call(
                "https://api.github.com/user/test/packages/container/test-package/versions?page=2&per_page=100",
                headers={
                    "Accept": "application/vnd.github+json",
                    "Authorization": "Bearer test-token",
                    "X-GitHub-Api-Version": "2022-11-28"
                }
            )
        ]
        mock_requests.get.assert_has_calls(expected_calls)
        
        # Verify the combined results from both pages
        self.assertEqual(len(result), 5)  # 3 from first page + 2 from second page
        
        # First three items should be from the first page
        for i in range(3):
            self.assertEqual(result[i]["id"], first_page_versions[i]["id"])
        
        # Last two items should be from the second page
        for i in range(2):
            self.assertEqual(result[i+3]["id"], second_page_versions[i]["id"])


class TestFindVersionsToClean(unittest.TestCase):
    def test_empty_versions_list(self):
        """Test with an empty list of versions"""
        cleanup_actions = find_versions_to_clean(versions=[], tagged_max_age=0, keep_tags_pattern="^test-pattern-", remove_all=False)
        self.assertEqual(len(cleanup_actions), 0)
    
    def test_find_untagged_versions(self):
        """Test finding versions with no tags"""
        created_at = fake.past_datetime(tzinfo=timezone.utc)
        tagged_created_at_iso = created_at.isoformat()
        orphan_created_at_iso = (created_at - timedelta(days=1)).isoformat()
        with_tags = [
            create_random_package_version(created_at=tagged_created_at_iso, metadata={"container": {"tags": ["tag1", "latest"]}})
            for _ in range(6)
        ]

        without_tags = [
            create_random_package_version(created_at=orphan_created_at_iso, metadata={"container": {"tags": []}}) 
            for _ in range(6)
        ]
        
        cleanup_actions = find_versions_to_clean(
            versions = with_tags + without_tags,
            tagged_max_age=datetime.now().timestamp() - created_at.timestamp() + 1,
            keep_tags_pattern="^preserve-",  # Different pattern
            remove_all=False
        )
        
        # All untagged versions should be marked for deletion
        self.assertEqual(len(cleanup_actions), len(with_tags) + len(without_tags))

        to_remove = [action for action in cleanup_actions if action["action"] == "delete"]
        self.assertEqual({a["version"]["id"] for a in to_remove}, {a["id"] for a in without_tags})
        
        # Verify the reason for deletion
        for action in cleanup_actions:
            if action["action"] == "delete":
                self.assertEqual(action["reason"], "Orphan version")
    
    def test_find_versions_with_git_commit_only(self):
        """Test finding versions with git commit only"""
        created_at = fake.past_datetime(tzinfo=timezone.utc)
        tagged_created_at_iso = created_at.isoformat()
        orphan_created_at_iso = (created_at - timedelta(days=1)).isoformat()
        git_commit_orphan_created_at_iso = (created_at - timedelta(days=2)).isoformat()
        with_tags = [
            create_random_package_version(created_at=tagged_created_at_iso, metadata={"container": {"tags": ["tag1", "latest"]}})
            for _ in range(6)
        ]

        without_tags = [
            create_random_package_version(created_at=orphan_created_at_iso, metadata={"container": {"tags": []}}) 
            for _ in range(6)
        ]

        with_git_commit_only_tags = [
            create_random_package_version(created_at=git_commit_orphan_created_at_iso, metadata={"container": {"tags": [f"git-commit-{i}"]}}) 
            for i in range(6)
        ]
        
        cleanup_actions = find_versions_to_clean(
            versions = with_tags + without_tags + with_git_commit_only_tags,
            tagged_max_age=datetime.now().timestamp() - created_at.timestamp() + 1,
            keep_tags_pattern="^preserve-",  # Different pattern
            remove_all=False
        )
        
        # All untagged versions should be marked for deletion
        self.assertEqual(len(cleanup_actions), len(with_tags) + len(without_tags) + len(with_git_commit_only_tags))

        to_remove = [action for action in cleanup_actions if action["action"] == "delete"]
        self.assertEqual({a["version"]["id"] for a in to_remove}, {a["id"] for a in (without_tags + with_git_commit_only_tags)})
        
        # Verify the reason for deletion
        for action in cleanup_actions:
            if action["action"] == "delete":
                self.assertEqual(action["reason"], "Orphan version")

    def test_delete_old_tagged_versions(self):
        """Test to delete old tagged versions"""
        now = datetime.now(timezone.utc)
        base_past = fake.past_datetime(tzinfo=timezone.utc)
        
        older_versions = []
        for i in range(4):
            version = create_random_package_version(
                id=1000 + i,
                created_at=(base_past - timedelta(hours=i*5) - timedelta(seconds=1)).isoformat()
            )
            older_versions.append(version)
        
        newer_versions = []
        for i in range(6):
            version = create_random_package_version(
                id=2000 + i,
                created_at=(base_past + timedelta(hours=i*5) + timedelta(seconds=1)).isoformat()
            )
            newer_versions.append(version)
        
        cleanup_actions = find_versions_to_clean(
            versions = older_versions + newer_versions,
            tagged_max_age=now.timestamp() - base_past.timestamp(),
            keep_tags_pattern="^keep-me-",  # Different pattern
            remove_all=False
        )
        
        to_remove = [action for action in cleanup_actions if action["action"] == "delete"]
        to_keep = [action for action in cleanup_actions if action["action"] == "keep"]
        
        self.assertEqual(len(to_remove), len(older_versions))
        self.assertEqual(len(to_keep), len(newer_versions))
        
        kept_ids = {v["version"]['id'] for v in to_keep}
        self.assertEqual(kept_ids, {v["id"] for v in newer_versions})

        removed_ids = {v["version"]['id'] for v in to_remove}
        self.assertEqual(removed_ids, {v["id"] for v in older_versions})
        
        for action in to_keep:
            self.assertIn("Tagged version newer than", action["reason"])

        for action in to_remove:
            self.assertIn("Tagged version older than", action["reason"])
    
    def test_keep_versions_matching_pattern(self):
        """Test keeping versions with tags matching pattern regardless of age"""
        now = datetime.now(timezone.utc)
        old_date = (now - timedelta(days=30)).isoformat()
        test_pattern = "^(archive-|stable-)"
        
        old_matching_versions = []
        for i in range(3):
            version = create_random_package_version(
                id=1000 + i,
                created_at=old_date,
                metadata={"container": {"tags": [f"archive-{i}", f"other-{i}"]}}
            )
            old_matching_versions.append(version)
            
        for i in range(3):
            version = create_random_package_version(
                id=2000 + i,
                created_at=old_date,
                metadata={"container": {"tags": [f"stable-v1.{i}.0", f"other-{i}"]}}
            )
            old_matching_versions.append(version)

        old_not_matching_versions = []
        for i in range(4):
            version = create_random_package_version(
                id=3000 + i,
                created_at=old_date,
                metadata={"container": {"tags": [f"v1.{i}.0", f"latest-{i}"]}}
            )
            old_not_matching_versions.append(version)
        
        cleanup_actions = find_versions_to_clean(
            versions=old_matching_versions + old_not_matching_versions,
            tagged_max_age=60 * 60 * 24 * 7,  # 7 days
            keep_tags_pattern=test_pattern,
            remove_all=False
        )
        
        to_keep = [action for action in cleanup_actions if action["action"] == "keep"]
        to_remove = [action for action in cleanup_actions if action["action"] == "delete"]
        
        self.assertEqual(len(to_keep), len(old_matching_versions))
        self.assertEqual(len(to_remove), len(old_not_matching_versions))
        
        kept_ids = {action["version"]["id"] for action in to_keep}
        expected_kept_ids = {v["id"] for v in old_matching_versions}
        self.assertEqual(kept_ids, expected_kept_ids)

        deleted_ids = {action["version"]["id"] for action in to_remove}
        expected_deleted_ids = {v["id"] for v in old_not_matching_versions}
        self.assertEqual(deleted_ids, expected_deleted_ids)
        
        for action in to_keep:
            self.assertIn("Tagged version matches keep pattern", action["reason"])
            self.assertIn(test_pattern, action["reason"])

    def test_keep_untagged_matching_timestamp(self):
        """Test keeping untagged versions if timestamp matches a kept tagged version"""
        now = datetime.now(timezone.utc)
        shared_timestamp = (now - timedelta(days=1))
        shared_timestamp_iso = shared_timestamp.isoformat()
        
        different_timestamp_iso = (now - timedelta(days=2)).isoformat()
        another_different_timestamp_iso = (now - timedelta(days=3)).isoformat()
        
        # Tagged version to keep (recent)
        manifest_list = create_random_package_version(
            id=1001,
            created_at=shared_timestamp_iso,
            metadata={"container": {"tags": ["latest", f"git-commit-{fake.sha1()[:7]}"]}}
        )
        
        # Untagged versions with matching timestamp (should be kept)
        orphan_amd64 = create_random_package_version(
            id=2001,
            created_at=(shared_timestamp + timedelta(seconds=3)).isoformat(),
            metadata={"container": {"tags": []}}
        )
        orphan_arm64 = create_random_package_version(
            id=2002,
            created_at=(shared_timestamp + timedelta(seconds=7)).isoformat(),
            metadata={"container": {"tags": []}}
        )
        
        # Untagged version with different timestamp (should be deleted)
        unrelated_orphan = create_random_package_version(
            id=3001,
            created_at=different_timestamp_iso,
            metadata={"container": {"tags": []}}
        )

        # Version tagged only with git-commit-* (treated as orphan, should be deleted)
        git_commit_orphan = create_random_package_version(
            id=3002,
            created_at=another_different_timestamp_iso,
            metadata={"container": {"tags": [f"git-commit-{fake.sha1()[:7]}"]}}
        )

        versions = [
            manifest_list, 
            orphan_amd64, 
            orphan_arm64, 
            unrelated_orphan, 
            git_commit_orphan
        ]
        
        # Set max age to keep the manifest list (e.g., 2 days)
        cleanup_actions = find_versions_to_clean(
            versions=versions,
            tagged_max_age=60 * 60 * 24 * 2, 
            keep_tags_pattern="^never-match-",
            remove_all=False
        )

        self.assertEqual(len(cleanup_actions), 5)

        actions_by_id = {action["version"]["id"]: action for action in cleanup_actions}

        # Check manifest list (kept because recent)
        self.assertEqual(actions_by_id[1001]["action"], "keep")
        self.assertIn("Tagged version newer than", actions_by_id[1001]["reason"])

        # Check orphans with matching timestamp (kept due to correlation)
        self.assertEqual(actions_by_id[2001]["action"], "keep")
        self.assertEqual(actions_by_id[2001]["reason"], f"Untagged version matches timestamp of kept version {manifest_list['id']}")
        self.assertEqual(actions_by_id[2002]["action"], "keep")
        self.assertEqual(actions_by_id[2002]["reason"], f"Untagged version matches timestamp of kept version {manifest_list['id']}")

        # Check unrelated orphan (deleted)
        self.assertEqual(actions_by_id[3001]["action"], "delete")
        self.assertEqual(actions_by_id[3001]["reason"], "Orphan version")
        
        # Check git-commit only orphan (deleted)
        self.assertEqual(actions_by_id[3002]["action"], "delete")
        self.assertEqual(actions_by_id[3002]["reason"], "Orphan version")

    def test_remove_all_versions(self):
        """Test the remove_all=True flag marks all versions for deletion"""
        now = datetime.now(timezone.utc)
        
        # Create a diverse set of versions
        versions = [
            # Old tagged, matching pattern
            create_random_package_version(id=1001, created_at=(now - timedelta(days=30)).isoformat(), metadata={"container": {"tags": ["keep-me-old"]}}),
            # New tagged, not matching pattern
            create_random_package_version(id=1002, created_at=(now - timedelta(days=1)).isoformat(), metadata={"container": {"tags": ["new-tag"]}}),
            # Old untagged
            create_random_package_version(id=1003, created_at=(now - timedelta(days=40)).isoformat(), metadata={"container": {"tags": []}}),
            # New untagged (timestamp close to a tagged one, but should still be deleted)
            create_random_package_version(id=1004, created_at=(now - timedelta(days=1, seconds=-5)).isoformat(), metadata={"container": {"tags": []}}),
            # Old git-commit only
            create_random_package_version(id=1005, created_at=(now - timedelta(days=50)).isoformat(), metadata={"container": {"tags": ["git-commit-abcdef"]}})
        ]
        
        cleanup_actions = find_versions_to_clean(
            versions=versions,
            tagged_max_age=60 * 60 * 24 * 7, # 7 days (irrelevant)
            keep_tags_pattern="^keep-me-",   # (irrelevant)
            remove_all=True
        )
        
        # Check that all versions are marked for deletion
        self.assertEqual(len(cleanup_actions), len(versions))
        
        for action in cleanup_actions:
            self.assertEqual(action["action"], "delete")
            self.assertEqual(action["reason"], "Marked for deletion by --all=yes-remove-all flag")

class TestRemoveVersion(unittest.TestCase):
    def test_remove_version_dry_run(self):
        """Test removing a version in dry run mode"""
        # Mock token provider
        mock_token_provider = MagicMock()
        mock_token_provider.get_token.return_value = "test-token"
        
        # Mock requests module
        mock_requests = MagicMock()
        
        # Call remove_version with dry_run=True
        result = remove_version(
            namespace="user/test",
            package_name="test-package",
            version_id=1234,
            dry_run=True,
            token_provider=mock_token_provider,
            requests_module=mock_requests
        )
        
        # Verify result is True
        self.assertTrue(result)
        
        # Verify no API calls were made
        mock_requests.delete.assert_not_called()
    
    def test_remove_version_actual(self):
        """Test removing a version for real"""
        # Mock token provider
        mock_token_provider = MagicMock()
        mock_token_provider.get_token.return_value = "test-token"
        
        # Mock requests module
        mock_response = MagicMock()
        mock_requests = MagicMock()
        mock_requests.delete.return_value = mock_response
        
        # Call remove_version with dry_run=False
        result = remove_version(
            namespace="user/test",
            package_name="test-package",
            version_id=1234,
            dry_run=False,
            token_provider=mock_token_provider,
            requests_module=mock_requests
        )
        
        # Verify result is True
        self.assertTrue(result)
        
        # Verify API was called correctly
        mock_requests.delete.assert_called_once_with(
            "https://api.github.com/user/test/packages/container/test-package/versions/1234",
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": "Bearer test-token",
                "X-GitHub-Api-Version": "2022-11-28"
            }
        )
        
    def test_remove_version_error(self):
        """Test error handling when removing a version"""
        # Mock token provider
        mock_token_provider = MagicMock()
        mock_token_provider.get_token.return_value = "test-token"
        
        # Mock requests module with an error response
        mock_response = MagicMock()
        mock_response.raise_for_status.side_effect = requests.exceptions.HTTPError("API Error")
        mock_requests = MagicMock()
        mock_requests.delete.return_value = mock_response
        
        # Call remove_version with dry_run=False
        with self.assertRaises(requests.exceptions.HTTPError):
            remove_version(
                namespace="user/test",
                package_name="test-package",
                version_id=1234,
                dry_run=False,
                token_provider=mock_token_provider,
                requests_module=mock_requests
            )

if __name__ == "__main__":
    unittest.main()
