#!/bin/bash

# Script to read a specific key from the build configuration file.

KEY_TO_READ=""

# Parse command line arguments
while [[ "$#" -gt 0 ]]; do
  case $1 in
    --key)
      if [[ -z "$2" ]]; then
        echo "Error: Argument for --key is missing" >&2
        exit 1
      fi
      KEY_TO_READ="$2"
      shift # past argument
      shift # past value
      ;;
    -*|--*)
      echo "Unknown option $1" >&2
      exit 1
      ;;
    *)
      # Ignore positional arguments if any, or handle them if needed
      shift
      ;;
  esac
done

# Check if the key was provided
if [[ -z "$KEY_TO_READ" ]]; then
  echo "Usage: $0 --key <config_key>" >&2
  exit 1
fi

SCRIPT_DIR=$(dirname "$0")
# Resolve the absolute path to the build config file relative to the script's location
BUILD_CFG_PATH=$(realpath "${SCRIPT_DIR}/../build.cfg")

# Check if the config file exists
if [[ ! -f "$BUILD_CFG_PATH" ]]; then
  echo "Error: Build config file not found at ${BUILD_CFG_PATH}" >&2
  exit 1
fi

# Read the value for the given key
# Use grep to find the line starting with the key, followed by optional spaces, '=', optional spaces
# Use cut to get the value after the first '=' (handles potential '=' in the value)
# Use sed to remove leading/trailing whitespace
VALUE=$(grep -m 1 "^${KEY_TO_READ}[[:space:]]*=[[:space:]]*" "${BUILD_CFG_PATH}" | cut -d'=' -f2- | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')


# Check if the value was found
if [[ -z "$VALUE" ]]; then
  # Check if the key line exists but has no value after '='
  if grep -q "^${KEY_TO_READ}[[:space:]]*=[[:space:]]*$" "${BUILD_CFG_PATH}"; then
    # Key exists but value is empty, print nothing and exit successfully
    echo ""
    exit 0
  else
    # Key does not exist in the file
    echo "Error: Key '${KEY_TO_READ}' not found in ${BUILD_CFG_PATH}" >&2
    exit 1
  fi
fi

# Print the value
echo "$VALUE"

exit 0 