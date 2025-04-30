#!/usr/bin/env python3

import os
import sys
import argparse
import requests
import subprocess
from typing import List, Optional, TypedDict, Callable, Protocol, Dict, Literal, Any
from datetime import datetime, timezone, timedelta
import logging
import re

class AuthenticationError(Exception):
    """Raised when authentication fails"""
    pass

class ContainerMetadata(TypedDict):
    tags: List[str]

class VersionMetadata(TypedDict):
    container: ContainerMetadata

class PackageVersion(TypedDict):
    id: int
    name: Optional[str]
    url: str
    package_html_url: str
    created_at: str
    updated_at: str
    html_url: str
    metadata: VersionMetadata

class CleanupAction(TypedDict):
    version: PackageVersion
    action: Literal["keep", "delete"]
    reason: str

class GitHubTokenProvider:
    """
    Class responsible for retrieving and caching GitHub tokens from various sources.
    """
    def __init__(self, environ=os.environ, subprocess_module=subprocess):
        """
        Initialize the token provider.
        
        Args:
            environ: Environment dictionary to use (default: os.environ)
            subprocess_module: Subprocess module to use (default: subprocess)
        """
        self._environ = environ
        self._subprocess = subprocess_module
        self._token = None
    
    def get_token(self) -> str:
        """
        Retrieve a GitHub token using multiple methods.
        
        1. Return cached token if available
        2. Check GITHUB_TOKEN environment variable
        3. Try to get token using GitHub CLI
        4. Raise AuthenticationError if all methods fail
        
        Returns:
            GitHub token as string
            
        Raises:
            AuthenticationError: If unable to retrieve a valid GitHub token
        """
        # Return cached token if we already retrieved it
        if self._token:
            return self._token
        
        # Method 1: Environment variable
        token = self._environ.get('GITHUB_TOKEN')
        if token:
            self._token = token
            return token
        
        # Method 2: GitHub CLI
        try:
            result = self._subprocess.run(
                ["gh", "auth", "token"], 
                capture_output=True, 
                text=True, 
                check=False
            )
            if result.returncode == 0 and result.stdout.strip():
                self._token = result.stdout.strip()
                return self._token
        except FileNotFoundError:
            # GitHub CLI not installed
            pass
        
        # All methods failed
        error_msg = (
            "Unable to retrieve GitHub token.\n"
            "Please either:\n"
            "  1. Set the GITHUB_TOKEN environment variable, or\n"
            "  2. Install and authenticate with GitHub CLI (gh)"
        )
        raise AuthenticationError(error_msg)
    
    def clear_token(self):
        """
        Clear the cached token, forcing a refresh on next get_token() call.
        """
        self._token = None

# Create a default token provider instance
default_token_provider = GitHubTokenProvider()

def list_versions(namespace: str, package_name: str, token_provider=default_token_provider, requests_module=requests) -> List[PackageVersion]:
    """
    List all versions of a package in the GitHub Container Registry.
    
    Args:
        namespace: The namespace in form of 'user/<username>' or 'org/<orgname>'
        package_name: The name of the package
        token_provider: Provider that returns a GitHub token (default: default_token_provider)
        requests_module: Module to use for HTTP requests (default: requests)
    
    Returns:
        List of package versions with metadata
        
    Raises:
        AuthenticationError: If authentication fails
        APIError: If API request fails
    """
    # Get authentication token
    token = token_provider.get_token()
    
    # Construct API URL
    api_url = f"https://api.github.com/{namespace}/packages/container/{package_name}/versions?per_page=100"
    
    # Set up headers with authentication
    headers = {
        "Accept": "application/vnd.github+json",
        "Authorization": f"Bearer {token}",
        "X-GitHub-Api-Version": "2022-11-28"
    }
    
    all_versions = []
    next_page = api_url
    
    # Fetch all pages
    while next_page:
        logging.info(f"Fetching versions from: {next_page}")
        response = requests_module.get(next_page, headers=headers)
        response.raise_for_status()
        all_versions.extend(response.json())
        
        # Check if there's a next page in Link header
        next_page = None
        if 'Link' in response.headers:
            links = response.headers['Link'].split(',')
            for link in links:
                if 'rel="next"' in link:
                    # Extract URL between < and >
                    match = re.search(r'<([^>]+)>', link)
                    if match:
                        next_page = match.group(1)
    
    return all_versions

def find_versions_to_clean(versions: List[PackageVersion], tagged_max_age: int, keep_tags_pattern: str, remove_all: bool = False) -> List[CleanupAction]:
    """
    Find package versions that should be cleaned up/removed.
    Keeps tagged versions newer than 'tagged_max_age' seconds.
    Keeps tagged versions with tags matching keep_tags_pattern.
    Keeps untagged versions if their creation timestamp closely matches a kept tagged version.
    If remove_all is True, marks all versions for deletion.
    
    Args:
        versions: List of package versions to analyze
        tagged_max_age: Maximum age in seconds for tagged versions to keep
        keep_tags_pattern: Regex pattern for tags to always keep regardless of age
        remove_all: If True, mark all versions for deletion (default: False)
        timestamp_tolerance_seconds: Tolerance in seconds for matching untagged to tagged timestamps
    
    Returns:
        List of cleanup actions with version, action ("keep" or "delete"), and reason
    """
    cleanup_actions = []
    
    # If remove_all flag is set, mark everything for deletion
    if remove_all:
        for version in versions:
            cleanup_actions.append({
                "version": version,
                "action": "delete",
                "reason": "Marked for deletion by --all=yes-remove-all flag"
            })
        return cleanup_actions

    # Separate versions into tagged and initially classified orphans
    tagged_versions = []
    potential_orphan_versions = [] # Initially includes truly untagged and git-commit-only

    git_commit_regex = re.compile(r"^git-commit-")
    timestamp_tolerance_seconds = 10 # Tolerance for timestamp matching
    
    for version in versions:
        tags = version.get('metadata', {}).get('container', {}).get('tags', [])
        has_real_tags = len(tags) > 0

        # Treat versions with only git-commit-* tags as untagged for initial classification
        if has_real_tags and len(tags) == 1 and git_commit_regex.match(tags[0]):
            has_real_tags = False

        if has_real_tags:
            tagged_versions.append(version)
        else:
            potential_orphan_versions.append(version)
    
    # Calculate cutoff date
    now = datetime.now(timezone.utc)
    tagged_max_age_delta = timedelta(seconds=tagged_max_age)
    cutoff_date = now - tagged_max_age_delta
    
    # Initialize result list and track kept tagged versions
    kept_tagged_versions_info = {} # Store id -> timestamp
    
    # Compile pattern if provided
    pattern = re.compile(keep_tags_pattern)
    
    # Process tagged versions first to determine which ones are kept
    for version in tagged_versions:
        tags = version.get('metadata', {}).get('container', {}).get('tags', [])
        created_date = datetime.fromisoformat(version['created_at'].replace('Z', '+00:00')) # Ensure timezone aware
        
        should_keep_due_to_pattern = False
        for tag in tags:
            if pattern.search(tag):
                should_keep_due_to_pattern = True
                break
        
        action = "delete" # Default to delete
        reason = f"Tagged version older than '{tagged_max_age_delta}'"

        if should_keep_due_to_pattern:
            action = "keep"
            reason = f"Tagged version matches keep pattern '{keep_tags_pattern}'"
        elif created_date > cutoff_date:
            action = "keep"
            reason = f"Tagged version newer than '{tagged_max_age_delta}'"

        if action == "keep":
            kept_tagged_versions_info[version['id']] = created_date

        cleanup_actions.append({
            "version": version,
            "action": action,
            "reason": reason
        })

    # Process potential orphan versions (untagged or git-commit-only)
    for version in potential_orphan_versions:
        action = "delete"
        reason = "Orphan version"
        
        # Check if this orphan's timestamp matches any kept tagged version's timestamp
        orphan_created_date = datetime.fromisoformat(version['created_at'].replace('Z', '+00:00'))
        time_tolerance = timedelta(seconds=timestamp_tolerance_seconds)
        
        for kept_id, kept_timestamp in kept_tagged_versions_info.items():
            if abs(orphan_created_date - kept_timestamp) <= time_tolerance:
                action = "keep"
                reason = f"Untagged version matches timestamp of kept version {kept_id}"
                break # Found a match, no need to check further kept versions

        cleanup_actions.append({
            "version": version,
            "action": action,
            "reason": reason
        })
    
    return cleanup_actions

def remove_version(namespace: str, package_name: str, version_id: int, 
                   dry_run: bool = True, token_provider=default_token_provider, 
                   requests_module=requests) -> bool:
    """
    Remove a specific package version from the GitHub Container Registry.
    
    Args:
        namespace: The namespace in form of 'user/<username>' or 'org/<orgname>'
        package_name: The name of the package
        version_id: The ID of the version to remove
        dry_run: If True, only simulate removal (default: True)
        token_provider: Provider that returns a GitHub token (default: default_token_provider)
        requests_module: Module to use for HTTP requests
    
    Returns:
        True if the version was removed (or would have been in dry_run mode)
        
    Raises:
        AuthenticationError: If authentication fails
        requests.exceptions.HTTPError: If API request fails
    """
    if dry_run:
        logging.info(f"[DRY RUN] Would remove version {version_id} of {namespace}/{package_name}")
        return True
    
    # Get authentication token
    token = token_provider.get_token()
    
    # Construct API URL
    api_url = f"https://api.github.com/{namespace}/packages/container/{package_name}/versions/{version_id}"
    
    # Set up headers with authentication
    headers = {
        "Accept": "application/vnd.github+json",
        "Authorization": f"Bearer {token}",
        "X-GitHub-Api-Version": "2022-11-28"
    }
    
    # Perform deletion
    response = requests_module.delete(api_url, headers=headers)
    response.raise_for_status()
    
    logging.info(f"Removed version {version_id} of {namespace}/{package_name}")
    return True

class CleanupArgs(Protocol):
    """Type definition for cleanup command arguments."""
    namespace: str
    package: str
    tagged_max_age: int
    really_remove: bool
    keep_tags_pattern: str
    all: Optional[str]

def cleanup_versions_command(args: CleanupArgs, 
                           list_versions_func: Callable[[str, str], List[PackageVersion]] = list_versions, 
                           find_versions_func: Callable[[List[PackageVersion], int, str, bool], List[CleanupAction]] = find_versions_to_clean, 
                           remove_version_func: Callable[[str, str, int, bool], bool] = remove_version):
    """
    Handle the cleanup-versions command logic.
    
    Args:
        args: Command line arguments from argparse with needed attributes
        list_versions_func: Function to list versions (default: list_versions)
        find_versions_func: Function to find versions to clean (default: find_versions_to_clean)
        remove_version_func: Function to remove a version (default: remove_version)
    """
    # Determine if all versions should be removed
    should_remove_all = args.all == "yes-remove-all"
    if args.all is not None and not should_remove_all:
      logging.error("Invalid value for --all flag. Must be '--all=yes-remove-all'.")
      sys.exit(1)
    
    # Get all versions
    all_versions = list_versions_func(args.namespace, args.package)
    
    # Find versions to clean
    cleanup_actions = find_versions_func(
        all_versions, 
        tagged_max_age=args.tagged_max_age,
        keep_tags_pattern=args.keep_tags_pattern,
        remove_all=should_remove_all
    )
    
    logging.info(f"Found {len(cleanup_actions)} versions from {args.namespace}/{args.package}:")
    for action in cleanup_actions:
      version = action["version"]
      name_display = version['name'] if version['name'] else 'N/A'
      tags = version['metadata']['container']['tags'] if 'container' in version['metadata'] else []
      logging.info(f"  - ID: {version['id']}, Name: {name_display}, Tags: {', '.join(tags)}")
      logging.info(f"    Created: {version['created_at']}")
      logging.info(f"    Action: {action['action']}")
      logging.info(f"    Reason: {action['reason']}")

    # Get versions to remove (those with action="delete")
    to_remove = [action["version"] for action in cleanup_actions if action["action"] == "delete"]
    
    # Show what would be removed
    if not to_remove:
        logging.info(f"No versions to remove from {args.namespace}/{args.package}.")
        return
    
    logging.info(f"Removing {len(to_remove)} (really_remove: {args.really_remove}) versions from {args.namespace}/{args.package}:")
    for version in to_remove:
        remove_version_func(args.namespace, args.package, version['id'], dry_run=not args.really_remove)
    logging.info(f"Successfully removed {len(to_remove)} versions.")

def main():
    parser = argparse.ArgumentParser(description="GitHub Container Registry (GHCR) CLI Tool")
    subparsers = parser.add_subparsers(dest="command", help="Commands")
    
    # List versions command
    list_parser = subparsers.add_parser("list-versions", help="List package versions")
    list_parser.add_argument("--namespace", required=True, help="Namespace in form 'user/<username>' or 'org/<orgname>'")
    list_parser.add_argument("--package", required=True, help="Package name")
    
    # Cleanup versions command
    cleanup_parser = subparsers.add_parser("cleanup-versions", help="Clean up old package versions")
    cleanup_parser.add_argument("--namespace", required=True, help="Namespace in form 'user/<username>' or 'org/<orgname>'")
    cleanup_parser.add_argument("--package", required=True, help="Package name")
    cleanup_parser.add_argument("--tagged-max-age", type=int, default=604800, help="Maximum age in seconds for tagged versions to keep (default: 604800 = 7 days)")
    cleanup_parser.add_argument("--really-remove", action="store_true", help="Actually perform deletion (without this flag, dry run is performed)")
    cleanup_parser.add_argument("--keep-tags-pattern", type=str, default="^(latest-|git-tag-)", 
                              help="Regex pattern for tags to always keep regardless of age (default: '^(latest-|git-tag-)')")
    cleanup_parser.add_argument("--all", type=str, default=None, help="If set to 'yes-remove-all', ignores all other rules and removes all versions.")
    
    args = parser.parse_args()
    
    # Configure logging
    logging.basicConfig(level=logging.INFO, format='%(levelname)s: %(message)s')
    
    # Check if no command is provided
    if args.command is None:
        parser.print_help()
        sys.exit(1)
    
    try:
        if args.command == "list-versions":
            versions = list_versions(args.namespace, args.package)
            logging.info(f"Found {len(versions)} versions for {args.namespace}/{args.package}:")
            for version in versions:
                logging.info(f"  - ID: {version['id']}")
                logging.info(f"    Name: {version['name'] if version['name'] else 'N/A'}")
                logging.info(f"    Created: {version['created_at']}")
                logging.info(f"    Updated: {version['updated_at']}")
                tags = version['metadata']['container']['tags'] if 'container' in version['metadata'] else []
                logging.info(f"    Tags: {', '.join(tags)}")
                logging.info("")
        
        elif args.command == "cleanup-versions":
            cleanup_versions_command(args)
    
    except Exception as e:
        logging.info(f"Command failed: {e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()
