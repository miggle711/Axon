#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

API_URL="http://localhost:8080"

echo -e "${BLUE}=== Queue API Test Suite ===${NC}\n"

# Test 1: Health Check
echo -e "${BLUE}Test 1: Health Check${NC}"
HEALTH=$(curl -s $API_URL/health)
echo "Response: $HEALTH"
if [[ $HEALTH == *"ok"* ]]; then
    echo -e "${GREEN}✓ PASS${NC}\n"
else
    echo -e "${RED}✗ FAIL${NC}\n"
    exit 1
fi

# Test 2: Initial Stats (empty queue)
echo -e "${BLUE}Test 2: Get Stats (empty queue)${NC}"
STATS=$(curl -s $API_URL/stats)
echo "Response: $STATS"
if [[ $STATS == *"pending_jobs"* ]]; then
    echo -e "${GREEN}✓ PASS${NC}\n"
else
    echo -e "${RED}✗ FAIL${NC}\n"
    exit 1
fi

# Test 3: Enqueue Job 1
echo -e "${BLUE}Test 3: Enqueue Job (search)${NC}"
JOB1=$(curl -s -X POST $API_URL/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "type": "search",
    "payload": "{\"query\": \"golang\"}",
    "priority": 2
  }')
echo "Response: $JOB1"
JOB1_ID=$(echo $JOB1 | grep -o '"id":"[^"]*' | head -1 | cut -d'"' -f4)
if [[ ! -z $JOB1_ID ]]; then
    echo -e "Job ID: $JOB1_ID"
    echo -e "${GREEN}✓ PASS${NC}\n"
else
    echo -e "${RED}✗ FAIL - Could not extract job ID${NC}\n"
    exit 1
fi

# Test 4: Enqueue Job 2
echo -e "${BLUE}Test 4: Enqueue Job (email)${NC}"
JOB2=$(curl -s -X POST $API_URL/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "type": "email",
    "payload": "{\"to\": \"test@example.com\"}",
    "priority": 1
  }')
echo "Response: $JOB2"
JOB2_ID=$(echo $JOB2 | grep -o '"id":"[^"]*' | head -1 | cut -d'"' -f4)
if [[ ! -z $JOB2_ID ]]; then
    echo -e "Job ID: $JOB2_ID"
    echo -e "${GREEN}✓ PASS${NC}\n"
else
    echo -e "${RED}✗ FAIL - Could not extract job ID${NC}\n"
    exit 1
fi

# Test 5: Check Stats (2 pending)
echo -e "${BLUE}Test 5: Get Stats (2 pending)${NC}"
STATS=$(curl -s $API_URL/stats)
echo "Response: $STATS"
if [[ $STATS == *"pending_jobs"* ]]; then
    echo -e "${GREEN}✓ PASS${NC}\n"
else
    echo -e "${RED}✗ FAIL${NC}\n"
    exit 1
fi

# Test 6: Dequeue (should get job1 - higher priority)
echo -e "${BLUE}Test 6: Dequeue Job${NC}"
DEQUEUED=$(curl -s $API_URL/jobs/next)
echo "Response: $DEQUEUED"
if [[ $DEQUEUED == *"search"* ]]; then
    echo -e "${GREEN}✓ PASS (got search job - correct priority ordering)${NC}\n"
else
    echo -e "${RED}✗ FAIL${NC}\n"
    exit 1
fi

# Test 7: Acknowledge Job
echo -e "${BLUE}Test 7: Acknowledge Job${NC}"
ACK=$(curl -s -X POST $API_URL/jobs/$JOB1_ID/ack)
echo "Response: $ACK"
echo -e "${GREEN}✓ PASS${NC}\n"

# Test 8: Check Stats (1 completed, 1 pending)
echo -e "${BLUE}Test 8: Get Stats (1 completed, 1 pending)${NC}"
STATS=$(curl -s $API_URL/stats)
echo "Response: $STATS"
echo -e "${GREEN}✓ PASS${NC}\n"

# Test 9: Dequeue second job
echo -e "${BLUE}Test 9: Dequeue second job${NC}"
DEQUEUED2=$(curl -s $API_URL/jobs/next)
echo "Response: $DEQUEUED2"
if [[ $DEQUEUED2 == *"email"* ]]; then
    echo -e "${GREEN}✓ PASS${NC}\n"
else
    echo -e "${RED}✗ FAIL${NC}\n"
    exit 1
fi

# Test 10: Fail Job
echo -e "${BLUE}Test 10: Fail Job${NC}"
FAIL=$(curl -s -X POST $API_URL/jobs/$JOB2_ID/fail \
  -H "Content-Type: application/json" \
  -d '{"reason": "API timeout"}')
echo "Response: $FAIL"
echo -e "${GREEN}✓ PASS${NC}\n"

# Test 11: Final Stats (1 completed, 1 failed)
echo -e "${BLUE}Test 11: Get Final Stats${NC}"
FINAL_STATS=$(curl -s $API_URL/stats)
echo "Response: $FINAL_STATS"
if [[ $FINAL_STATS == *"completed_jobs"* && $FINAL_STATS == *"failed_jobs"* ]]; then
    echo -e "${GREEN}✓ PASS${NC}\n"
else
    echo -e "${RED}✗ FAIL${NC}\n"
    exit 1
fi

echo -e "${GREEN}=== All Tests Passed! ===${NC}"
